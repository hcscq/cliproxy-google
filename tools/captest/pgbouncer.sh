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
default_pool_size = 25
reserve_pool_size = 5
max_db_connections = 30
max_prepared_statements = 500
ignore_startup_parameters = extra_float_digits,search_path
logfile = $D/pgbouncer.log
pidfile = $D/pgbouncer.pid
unix_socket_dir = $D
INI
  echo '"postgres" ""' > $D/userlist.txt
  /usr/sbin/pgbouncer -d $D/pgbouncer.ini
}
down() { [ -f $D/pgbouncer.pid ] && kill $(cat $D/pgbouncer.pid) 2>/dev/null; sleep 1; }
case "$1" in up) up;; down) down;; restart) down; up;; esac
