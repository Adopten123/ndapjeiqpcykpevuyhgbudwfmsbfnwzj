#!/bin/sh
(
    set -eu
    psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set=ON_ERROR_STOP=1 \
        --set=app_password="$(cat /run/rzd-database/app-password)" <<'SQL'
CREATE ROLE rzd_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'app_password';
ALTER DATABASE rzd_arctica_demo OWNER TO rzd_app;
SQL
)
