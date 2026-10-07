"""The api: GET /books lists the books, POST /books queues one for the
worker, and GET /health answers once the api is up."""
import json
import os
import sqlite3
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

path = sys.argv[1]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"ok": True})
        if self.path == "/books":
            with sqlite3.connect(path) as db:
                rows = db.execute("SELECT id, title, status FROM books ORDER BY id").fetchall()
            return self.reply(200, [{"id": i, "title": t, "status": s} for i, t, s in rows])
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/books":
            return self.reply(404, {"error": "not found"})
        length = int(self.headers.get("Content-Length", 0))
        try:
            title = json.loads(self.rfile.read(length) or b"{}").get("title")
        except ValueError:
            title = None
        if not title:
            return self.reply(400, {"error": 'send {"title": "..."}'})
        with sqlite3.connect(path) as db:
            book = db.execute("INSERT INTO books (title, status) VALUES (?, 'queued')", (title,)).lastrowid
        print(f"queued {title!r}", flush=True)
        self.reply(201, {"id": book, "title": title, "status": "queued"})

    def reply(self, code, body):
        data = json.dumps(body).encode() + b"\n"
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        pass  # the journal gets the lines above, not one per request


port = int(os.environ.get("PORT", "8080"))
print(f"api on 127.0.0.1:{port}", flush=True)
ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
