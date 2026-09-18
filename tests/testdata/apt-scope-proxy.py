#!/usr/bin/env python3
"""Disposable loopback HTTP observer; logs credential identity, never its value."""
import base64
import http.server
import json
import os
import pathlib
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request

upstream, output = sys.argv[1:]
if urllib.parse.urlsplit(upstream).hostname not in ("127.0.0.1", "localhost", "::1"):
    raise SystemExit("qualification proxy requires loopback upstream")
root = pathlib.Path(output)
lock = threading.Lock()
authorizations = {
    label: "Basic " + base64.b64encode(("reader:" + os.environ[key]).encode()).decode()
    for label, key in (("a", "READER_A"), ("b", "READER_B"))
}

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        request = urllib.request.Request(upstream + self.path)
        for key in ("Authorization", "Range", "If-Modified-Since", "If-None-Match"):
            if self.headers.get(key):
                request.add_header(key, self.headers[key])
        try:
            response = urllib.request.urlopen(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read()
            self.send_response(response.status)
            for key in ("Content-Type", "WWW-Authenticate", "Last-Modified", "ETag"):
                if response.headers.get(key):
                    self.send_header(key, response.headers[key])
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            event = {"path": self.path, "status": response.status,
                     "phase": (root / "phase").read_text().strip(),
                     "auth": bool(auth), "a": auth == authorizations["a"],
                     "b": auth == authorizations["b"]}
            with lock, (root / "requests.jsonl").open("a") as f:
                f.write(json.dumps(event) + "\n")
            self.wfile.write(body)

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
(root / "origin").write_text("http://127.0.0.1:" + str(server.server_port))
server.serve_forever()
