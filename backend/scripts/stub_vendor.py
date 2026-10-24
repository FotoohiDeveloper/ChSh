#!/usr/bin/env python3
"""Stand-in for the identity-inquiry and SMS vendors, for smoke tests only.
POST /verify -> {"match": true, "full_name": "..."}   POST /send -> {"id": "stub-N"}
Every request body is appended to the file given as argv[2] (one JSON per line)."""
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

port, log_path = int(sys.argv[1]), sys.argv[2]
counter = 0

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        global counter
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode()
        auth = self.headers.get("Authorization", "")
        with open(log_path, "a") as f:
            f.write(json.dumps({"path": self.path, "auth": auth, "body": json.loads(body or "{}")}, ensure_ascii=False) + "\n")
        if auth != "Bearer stub-token":
            self.send_response(401); self.end_headers(); return
        counter += 1
        out = {"match": True, "full_name": "علی رضایی"} if self.path == "/verify" else {"id": f"stub-{counter}"}
        data = json.dumps(out, ensure_ascii=False).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(data)
    def log_message(self, *a): pass

HTTPServer(("127.0.0.1", port), H).serve_forever()
