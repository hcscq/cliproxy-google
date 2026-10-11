#!/bin/bash
# usage: pgbouncer.sh up|down|restart — local pgbouncer (transaction mode) on 10.201.0.1:56433 -> PG 56432
D=/workspace/cap/run/pgbouncer
up() {
  mkdir -p $D
  cat > $D/pgbouncer.ini <<INI
[databases]
home = host=127.0.0.1 port=56432 dbname=home
[pgbouncer]
listen_addr = 10.201.0.1,127.0.0.1
listen_port = 56433
auth_type = trust
auth_file = $D/userlist.txt
pool_mode = transaction
max_client_conn = 2000
default_pool_size = ${PGB_POOL:-25}
reserve_pool_size = 5
max_db_connections = ${PGB_MAX:-30}
max_prepared_statements = 500
ignore_startup_parameters = extra_float_digits,search_path
logfile = $D/pgbouncer.log
pidfile = $D/pgbouncer.pid
unix_socket_dir = $D
INI
  echo '"postgres" ""' > $D/userlist.txt
  /usr/sbin/pgbouncer -d $D/pgbouncer.ini
}
down() {  # SIGINT = immediate shutdown (SIGTERM waits for clients)
  [ -f $D/pgbouncer.pid ] || return 0
  p=$(cat $D/pgbouncer.pid); kill -INT $p 2>/dev/null
  for i in $(seq 1 50); do kill -0 $p 2>/dev/null || break; sleep 0.1; done
  rm -f $D/pgbouncer.pid
}
case "$1" in up) up;; down) down;; restart) down; up;; esac
