"""CPA Bright Data ISP relay.

Listens on loopback and forwards raw TCP to brd.superproxy.io, binding every
upstream connection to this node's secondary IPv4 (the address that must be on
the Bright Data allowlist). Squid in the same pod is the only client.

This is CPA's own data plane. It does not depend on kiro's :3129 at runtime.
The relay never parses or logs proxy credentials (username/password PASSTHRU
happens in Squid).
"""
import asyncio
import ipaddress
import os
import socket


LISTEN_HOST = os.getenv("LISTEN_HOST", "127.0.0.1")
LISTEN_PORT = int(os.getenv("LISTEN_PORT", "44447"))
UPSTREAM_HOST = os.getenv("UPSTREAM_HOST", "brd.superproxy.io")
UPSTREAM_PORT = int(os.getenv("UPSTREAM_PORT", "44445"))
CONNECT_TIMEOUT = float(os.getenv("CONNECT_TIMEOUT", "10"))
# Upstream connect attempts before giving up. Retries happen before any byte is
# read from Squid, so a transient Bright Data connect failure never surfaces as
# a CONNECT rejection (which kiro.rs would otherwise count as egress evidence).
CONNECT_ATTEMPTS = max(1, int(os.getenv("CONNECT_ATTEMPTS", "3")))
CONNECT_RETRY_BACKOFF = float(os.getenv("CONNECT_RETRY_BACKOFF", "0.5"))
TCP_KEEPALIVE_IDLE = int(os.getenv("TCP_KEEPALIVE_IDLE", "60"))
IDLE_TIMEOUT = float(os.getenv("IDLE_TIMEOUT", "900"))
OUTBOUND_SOURCE_IP = os.getenv("OUTBOUND_SOURCE_IP", "").strip()
OUTBOUND_INTERFACE = os.getenv("OUTBOUND_INTERFACE", "").strip()
OUTBOUND_SOURCE_REFRESH_SECONDS = float(
    os.getenv("OUTBOUND_SOURCE_REFRESH_SECONDS", "60")
)
OUTBOUND_SOURCE_PREFERRED_IP = os.getenv("OUTBOUND_SOURCE_PREFERRED_IP", "").strip()
OUTBOUND_SOURCE_STATE_FILE = os.getenv("OUTBOUND_SOURCE_STATE_FILE", "").strip()


class OutboundSourceResolver:
    """Select the outbound source IPv4 for this node and keep it fixed.

    Priority:
      1. OUTBOUND_SOURCE_IP (explicit pin, no discovery at all).
      2. Sticky: once an IP is selected it is kept for the life of the process as
         long as it is still configured on the interface. New addresses appearing
         never cause a switch.
      3. Initial selection without a pin:
         a. persisted last-used IP (OUTBOUND_SOURCE_STATE_FILE) if still present;
         b. exactly one 'secondary' address (legacy behaviour);
         c. OUTBOUND_SOURCE_PREFERRED_IP (e.g. node ExternalIP) if present among
            the global addresses;
         d. otherwise fail closed with a clear error.
      If the selected IP disappears from the interface we fail closed and never
      jump to a different address.
    """

    def __init__(
        self,
        source_ip: str,
        interface: str,
        refresh_seconds: float,
        preferred_ip: str = "",
        state_file: str = "",
    ) -> None:
        self.source_ip = self._validate_ip(source_ip) if source_ip else None
        self.interface = interface
        self.refresh_seconds = max(refresh_seconds, 1.0)
        self.preferred_ip = self._validate_ip(preferred_ip) if preferred_ip else None
        self.state_file = state_file
        self.selected_ip = None
        self.cached_ip = None
        self.cache_expires_at = 0.0
        self.lock = asyncio.Lock()

    @staticmethod
    def _validate_ip(value: str) -> str:
        address = ipaddress.ip_address(value)
        if address.version != 4:
            raise ValueError("only IPv4 outbound source addresses are supported")
        return str(address)

    @property
    def dynamic(self) -> bool:
        return self.source_ip is None and bool(self.interface)

    def _load_state(self):
        if not self.state_file:
            return None
        try:
            with open(self.state_file, "r", encoding="utf-8") as handle:
                return self._validate_ip(handle.read().strip())
        except (OSError, ValueError):
            return None

    def _save_state(self, address: str) -> None:
        if not self.state_file:
            return
        try:
            tmp = self.state_file + ".tmp"
            with open(tmp, "w", encoding="utf-8") as handle:
                handle.write(address + "\n")
            os.replace(tmp, self.state_file)
        except OSError as exc:
            print(f"WARNING: cannot persist outbound source: {exc}", flush=True)

    def select(self, global_addresses, secondary_addresses) -> str:
        """Pure selection logic (unit-tested)."""
        present = set(global_addresses)
        if self.selected_ip:
            if self.selected_ip in present:
                return self.selected_ip
            raise RuntimeError(
                f"selected outbound source {self.selected_ip} is no longer on "
                f"{self.interface}; refusing to switch to another address "
                f"(set OUTBOUND_SOURCE_IP to choose explicitly)"
            )

        persisted = self._load_state()
        if persisted and persisted in present:
            return persisted

        candidates = list(secondary_addresses)
        if not candidates and len(global_addresses) == 2:
            candidates = list(global_addresses[1:])
        if len(candidates) == 1:
            return candidates[0]

        if len(candidates) > 1 and self.preferred_ip and self.preferred_ip in present:
            print(
                f"WARNING: {len(candidates)} secondary IPv4 addresses on "
                f"{self.interface}; using OUTBOUND_SOURCE_PREFERRED_IP "
                f"{self.preferred_ip}",
                flush=True,
            )
            return self.preferred_ip

        raise RuntimeError(
            f"expected exactly one secondary IPv4 address on {self.interface}, "
            f"found {len(candidates)} ({', '.join(candidates)}); "
            f"set OUTBOUND_SOURCE_IP to pin one"
        )

    async def resolve(self, force: bool = False) -> str | None:
        if self.source_ip:
            return self.source_ip
        if not self.interface:
            return None

        now = asyncio.get_running_loop().time()
        if not force and self.cached_ip and now < self.cache_expires_at:
            return self.cached_ip

        async with self.lock:
            now = asyncio.get_running_loop().time()
            if not force and self.cached_ip and now < self.cache_expires_at:
                return self.cached_ip

            global_addresses, secondary_addresses = await self._list_addresses()
            try:
                resolved_ip = self.select(global_addresses, secondary_addresses)
            except RuntimeError:
                self.cached_ip = None
                raise
            if resolved_ip != self.selected_ip:
                print(
                    f"outbound source on {self.interface}: {resolved_ip}",
                    flush=True,
                )
                self.selected_ip = resolved_ip
                self._save_state(resolved_ip)
            self.cached_ip = resolved_ip
            self.cache_expires_at = now + self.refresh_seconds
            return resolved_ip

    @classmethod
    def parse_addresses(cls, text: str):
        global_addresses = []
        secondary_addresses = []
        for line in text.splitlines():
            fields = line.split()
            try:
                inet_index = fields.index("inet")
                address = cls._validate_ip(fields[inet_index + 1].split("/", 1)[0])
            except (ValueError, IndexError):
                continue
            global_addresses.append(address)
            if "secondary" in fields:
                secondary_addresses.append(address)
        return global_addresses, secondary_addresses

    async def _list_addresses(self):
        process = await asyncio.create_subprocess_exec(
            "ip", "-4", "-o", "address", "show", "dev", self.interface,
            "scope", "global",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        try:
            stdout, stderr = await asyncio.wait_for(
                process.communicate(), timeout=CONNECT_TIMEOUT
            )
        except TimeoutError:
            process.kill()
            await process.wait()
            raise RuntimeError(
                f"timed out discovering addresses on {self.interface}"
            )
        if process.returncode != 0:
            detail = stderr.decode("utf-8", "replace").strip()
            raise RuntimeError(
                f"failed to discover addresses on {self.interface}: {detail}"
            )
        return self.parse_addresses(stdout.decode("utf-8", "replace"))

    def invalidate(self) -> None:
        self.cache_expires_at = 0.0


outbound_source = OutboundSourceResolver(
    OUTBOUND_SOURCE_IP,
    OUTBOUND_INTERFACE,
    OUTBOUND_SOURCE_REFRESH_SECONDS,
    OUTBOUND_SOURCE_PREFERRED_IP,
    OUTBOUND_SOURCE_STATE_FILE,
)


def enable_keepalive(writer):
    """Detect dead peers on long-lived tunnels instead of waiting for IDLE_TIMEOUT."""
    sock = writer.get_extra_info("socket")
    if sock is None:
        return
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_KEEPALIVE, 1)
        if hasattr(socket, "TCP_KEEPIDLE"):
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_KEEPIDLE, TCP_KEEPALIVE_IDLE)
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_KEEPINTVL, 15)
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_KEEPCNT, 4)
    except OSError:
        pass


async def open_upstream():
    last_error = None
    for attempt in range(1, CONNECT_ATTEMPTS + 1):
        try:
            return await open_upstream_once()
        except (OSError, asyncio.TimeoutError) as error:
            last_error = error
            if attempt < CONNECT_ATTEMPTS:
                print(
                    f"upstream connect attempt {attempt}/{CONNECT_ATTEMPTS} failed: {error!r}",
                    flush=True,
                )
                await asyncio.sleep(CONNECT_RETRY_BACKOFF * attempt)
    raise last_error


async def open_upstream_once():
    source_ip = await outbound_source.resolve()
    try:
        return await asyncio.wait_for(
            asyncio.open_connection(
                UPSTREAM_HOST,
                UPSTREAM_PORT,
                local_addr=(source_ip, 0),
            ),
            timeout=CONNECT_TIMEOUT,
        )
    except OSError:
        if outbound_source.dynamic:
            # Re-verify the sticky IP is still present; raises (fail closed) if
            # it disappeared. Never switches to a different address.
            outbound_source.invalidate()
            await outbound_source.resolve(force=True)
        raise


async def copy_stream(reader, writer):
    try:
        while True:
            data = await asyncio.wait_for(reader.read(65536), timeout=IDLE_TIMEOUT)
            if not data:
                return
            writer.write(data)
            await writer.drain()
    finally:
        await finish_writer(writer)


async def finish_writer(writer):
    if writer.can_write_eof():
        try:
            writer.write_eof()
            await writer.drain()
            return
        except (ConnectionError, OSError):
            pass
    await close_writer(writer)


async def close_writer(writer):
    if writer is None:
        return
    writer.close()
    try:
        await writer.wait_closed()
    except Exception:
        pass


async def handle_client(client_reader, client_writer):
    upstream_writer = None
    try:
        upstream_reader, upstream_writer = await open_upstream()
        enable_keepalive(client_writer)
        enable_keepalive(upstream_writer)
        await asyncio.gather(
            copy_stream(client_reader, upstream_writer),
            copy_stream(upstream_reader, client_writer),
        )
    except Exception as error:
        print(f"relay connection failed: {error}", flush=True)
    finally:
        await close_writer(upstream_writer)
        await close_writer(client_writer)


async def main():
    source_ip = await outbound_source.resolve(force=True)
    _, preflight_writer = await open_upstream()
    bound_address = preflight_writer.get_extra_info("sockname")
    await close_writer(preflight_writer)
    if not bound_address or bound_address[0] != source_ip:
        raise RuntimeError(
            f"upstream preflight used {bound_address}; expected source {source_ip}"
        )
    server = await asyncio.start_server(handle_client, LISTEN_HOST, LISTEN_PORT)
    print(
        f"relay listening on {LISTEN_HOST}:{LISTEN_PORT}; "
        f"upstream={UPSTREAM_HOST}:{UPSTREAM_PORT}; source={source_ip}",
        flush=True,
    )
    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    asyncio.run(main())
