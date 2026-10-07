"""The worker: catalogues each queued book, one at a time."""
import sqlite3
import sys
import time

path = sys.argv[1]
print("worker started", flush=True)
while True:
    with sqlite3.connect(path) as db:
        book = db.execute("SELECT id, title FROM books WHERE status = 'queued' ORDER BY id LIMIT 1").fetchone()
        if book:
            db.execute("UPDATE books SET status = 'catalogued' WHERE id = ?", (book[0],))
    if book:
        print(f"catalogued {book[1]!r}", flush=True)
    else:
        time.sleep(1)
