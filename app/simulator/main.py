"""Запустить учебный имитатор на локальном интерфейсе."""
import argparse
from pathlib import Path
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deps", type=Path, help="Каталог зависимостей FastAPI/Uvicorn")
    parser.add_argument("--port", type=int, default=8094)
    parser.add_argument("--host", choices=("127.0.0.1", "0.0.0.0", "::1"), default="127.0.0.1")
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("port must be 1..65535")
    if args.deps:
        sys.path.insert(0, str(args.deps.resolve()))

    import uvicorn
    from server import app

    uvicorn.run(app, host=args.host, port=args.port, workers=1, proxy_headers=False,
                server_header=False, access_log=False)


if __name__ == "__main__":
    main()
