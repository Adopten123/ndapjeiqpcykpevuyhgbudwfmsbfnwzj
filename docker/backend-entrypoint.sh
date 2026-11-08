#!/bin/sh
set -eu
password="$(cat /run/rzd-database/app-password)"
export RZD_DATABASE_URL="postgres://rzd_app:${password}@postgres:5432/rzd_arctica_demo?sslmode=disable"
unset password
exec /usr/local/bin/server
