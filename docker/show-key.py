"""Показать ключ координатора сохранённого Docker-контура."""
import json
from pathlib import Path
import sys


try:
    document = json.loads(Path("/workspace/.local/credentials.private.json").read_text(encoding="utf-8"))
    user = next(user for user in document["users"] if user["id"] == "coordinator.demo")
    print(user["access_key"])
except (OSError, ValueError, KeyError, TypeError, StopIteration):
    print("Ключ координатора ещё не создан. Выполните make up.", file=sys.stderr)
    raise SystemExit(1)
