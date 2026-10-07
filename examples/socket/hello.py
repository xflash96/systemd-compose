"""An HTTP server on the socket systemd hands it (LISTEN_FDS)."""
import os
import socket
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

if os.environ.get("LISTEN_PID") != str(os.getpid()) or os.environ.get("LISTEN_FDS") != "1":
    sys.exit("hello: no socket from systemd; start it with systemd-compose up")
started = time.strftime("%H:%M:%S")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = f"hello from pid {os.getpid()}, started at {started}\n".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler, bind_and_activate=False)
server.socket.close()
server.socket = socket.socket(fileno=3)  # systemd's first socket
print(f"hello serving on {server.socket.getsockname()}", flush=True)
server.serve_forever()
