"""Инициализация сохранённых Docker-томов без замены существующих ключей."""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys


AUTH = Path("/workspace/.local")
DATABASE = Path("/run/rzd-database")


def password_file(name: str) -> None:
    path = DATABASE / name
    if path.exists():
        if path.is_symlink() or not re.fullmatch(r"[a-f0-9]{64}", path.read_text().strip()):
            raise ValueError("Повреждён файл пароля PostgreSQL в сохранённом томе.")
        return
    value = secrets.token_hex(32) + "\n"
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o444)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        stream.write(value)
        stream.flush()
        os.fsync(stream.fileno())


def validate_auth(config: Path, credentials: Path) -> None:
    policy = json.loads(config.read_text(encoding="utf-8"))
    keys = json.loads(credentials.read_text(encoding="utf-8"))
    if policy.get("scope") != "demo-chain" or keys.get("scope") != "demo-chain":
        raise ValueError("Неверный контур сохранённых ключей.")
    users = policy["users"]
    key_users = keys["users"]
    by_id = {user["id"]: user for user in key_users}
    if not users or len(by_id) != len(key_users) or len(users) != len(by_id):
        raise ValueError("Сохранённые файлы ключей не согласованы.")
    now = datetime.now(timezone.utc)
    for user in users:
        key = by_id[user["id"]]["access_key"]
        if hashlib.sha256(key.encode()).hexdigest() != user["token_sha256"]:
            raise ValueError("Сохранённые файлы ключей не согласованы.")
        if datetime.fromisoformat(user["valid_until"]) <= now:
            raise ValueError(
                "Учебные ключи в Docker-томе истекли. Для нового независимого "
                "контура задайте другое COMPOSE_PROJECT_NAME и повторите make up."
            )


def main() -> None:
    AUTH.mkdir(mode=0o700, exist_ok=True)
    DATABASE.mkdir(mode=0o755, exist_ok=True)
    config = AUTH / "auth.private.json"
    credentials = AUTH / "credentials.private.json"
    if config.exists() != credentials.exists():
        raise ValueError("В Docker-томе сохранился неполный комплект ключей. Проверьте том auth-data.")
    if not config.exists():
        subprocess.run(
            ["/usr/local/bin/demo-auth", "-output", str(config),
             "-credentials", str(credentials), "-valid-for", "168h"],
            check=True, stdout=subprocess.DEVNULL,
        )
    validate_auth(config, credentials)
    password_file("admin-password")
    password_file("app-password")
    print("Учебные ключи и пароли PostgreSQL готовы. Сохранённые значения используются повторно.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f"Инициализация Docker-контура остановлена: {error}", file=sys.stderr)
        raise SystemExit(1)
