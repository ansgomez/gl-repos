"""Stand-in for Ollama in the demo recording (docs/demo/setup.sh).

Answers every chat request with the same plan for the billing-service
changes, streamed in small chunks like a real model. No model, no network:
it listens on 127.0.0.1 only.

    python3 fake_ollama.py <port>
"""
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL = {"name": "gemma4:e2b", "model": "gemma4:e2b", "size": 1 << 30}
PLAN = {
    "summary": "Commit the retry logic and its test",
    "clone": "",
    "steps": [
        {"op": "stage", "paths": ["charge/charge.go", "charge/charge_test.go"]},
        {"op": "commit", "message": "Retry failed charges with exponential backoff"},
    ],
}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, obj):
        body = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.reply({"models": [MODEL]})  # /api/ps and /api/tags

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        if self.path != "/api/chat":
            return self.reply({"done": True})  # unload requests
        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson")
        self.end_headers()
        text = json.dumps(PLAN)
        time.sleep(0.8)  # "loading the model"
        for i in range(0, len(text), 12):
            chunk = {"message": {"role": "assistant", "content": text[i:i + 12]}, "done": False}
            self.wfile.write((json.dumps(chunk) + "\n").encode()); self.wfile.flush()
            time.sleep(0.04)
        self.wfile.write((json.dumps({"message": {"role": "assistant", "content": ""}, "done": True}) + "\n").encode())


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
