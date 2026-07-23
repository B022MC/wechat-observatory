#!/bin/sh
set -eu

required() {
  value=$(eval "printf '%s' \"\${$1:-}\"")
  if [ -z "$value" ]; then
    echo "$1 is required" >&2
    exit 1
  fi
}

for key in OBS_DB_NAME OBS_DB_USER OBS_DB_PASSWORD GATEWAY_DB_NAME GATEWAY_DB_USER GATEWAY_DB_PASSWORD; do
  required "$key"
done

identifier() {
  case "$1" in
    *[!A-Za-z0-9_]* | '')
      echo "invalid MySQL identifier" >&2
      exit 1
      ;;
  esac
}

literal() {
  printf '%s' "$1" | sed "s/'/''/g"
}

identifier "$OBS_DB_NAME"
identifier "$OBS_DB_USER"
identifier "$GATEWAY_DB_NAME"
identifier "$GATEWAY_DB_USER"

mysql -uroot -p"$MYSQL_ROOT_PASSWORD" <<SQL
CREATE DATABASE IF NOT EXISTS \`$OBS_DB_NAME\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS \`$GATEWAY_DB_NAME\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '$(literal "$OBS_DB_USER")'@'%' IDENTIFIED BY '$(literal "$OBS_DB_PASSWORD")';
CREATE USER IF NOT EXISTS '$(literal "$GATEWAY_DB_USER")'@'%' IDENTIFIED BY '$(literal "$GATEWAY_DB_PASSWORD")';
GRANT ALL PRIVILEGES ON \`$OBS_DB_NAME\`.* TO '$(literal "$OBS_DB_USER")'@'%';
GRANT ALL PRIVILEGES ON \`$GATEWAY_DB_NAME\`.* TO '$(literal "$GATEWAY_DB_USER")'@'%';
GRANT REPLICATION CLIENT ON *.* TO '$(literal "$GATEWAY_DB_USER")'@'%';
FLUSH PRIVILEGES;
SQL
