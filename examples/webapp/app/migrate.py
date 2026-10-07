"""Creates the database, or leaves it as it is. Safe to run again."""
import os
import sqlite3
import sys

path = sys.argv[1]
os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
with sqlite3.connect(path) as db:
    db.execute(
        "CREATE TABLE IF NOT EXISTS books"
        " (id INTEGER PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL)"
    )
print(f"database ready: {path}", flush=True)
