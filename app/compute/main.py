"""Локальный запуск внутреннего FastAPI, без установки системной службы."""
import argparse
from pathlib import Path
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deps", type=Path, help="Каталог зависимостей FastAPI/Uvicorn")
    parser.add_argument("--planner-deps", type=Path, help="Каталог OR-Tools/jsonschema")
    parser.add_argument("--port", type=int, default=8091)
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("port must be 1..65535")
    if args.planner_deps:
        sys.path.insert(0, str(args.planner_deps.resolve()))
    if args.deps:
        sys.path.insert(0, str(args.deps.resolve()))
    import uvicorn
    from server import create_app
    uvicorn.run(create_app(), host="127.0.0.1", port=args.port, workers=1, proxy_headers=False,
                server_header=False, access_log=False)


if __name__ == "__main__":
    main()
