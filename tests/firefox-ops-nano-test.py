#!/usr/bin/env python3
"""Marionette smoke fixture for a real local /ops candidate.

The candidate URL and operator session are supplied out of band through
TPARUN_BROWSER_URL and TPARUN_BROWSER_SESSION. The fixture intentionally
asserts effective finite persisted entitlement values.
"""
import json, os, socket, sys

url = os.environ["TPARUN_BROWSER_URL"].rstrip("/")
session = os.environ["TPARUN_BROWSER_SESSION"]
expected = json.loads(os.environ.get("TPARUN_BROWSER_EXPECTED", "{}"))
host, port = os.environ.get("MARIONETTE", "127.0.0.1:2828").split(":")
s = socket.create_connection((host, int(port)), 10)

def recv():
    b = b""
    while not b.endswith(b":"): b += s.recv(1)
    n, data = int(b[:-1]), b""
    while len(data) < n: data += s.recv(n - len(data))
    return json.loads(data)

def call(i, name, params):
    raw = json.dumps([0, i, name, params], separators=(",", ":")).encode()
    s.sendall(str(len(raw)).encode() + b":" + raw)
    return recv()

recv()
call(1, "WebDriver:NewSession", {"capabilities": {"alwaysMatch": {"browserName": "firefox"}}})
call(2, "WebDriver:Navigate", {"url": url + "/ops?scope=all"})
anonymous = call(3, "WebDriver:ExecuteScript", {"script": "return {title: document.title, body: document.body.innerText};", "args": []})
anonymous_value = anonymous[3]["value"]
if "Service dashboard" in anonymous_value["body"]:
    raise AssertionError("anonymous Firefox session reached /ops")
call(4, "WebDriver:DeleteSession", {})
call(5, "WebDriver:NewSession", {"capabilities": {"alwaysMatch": {"browserName": "firefox"}}})
call(6, "WebDriver:AddCookie", {"cookie": {"name": "tpahub_operator_session", "value": session, "path": "/ops"}})
call(7, "WebDriver:Navigate", {"url": url + "/ops?scope=all"})
script = """
return {resources: [...document.querySelectorAll('.resource-usage')].map(e => ({
  text:e.textContent, state:e.dataset.limitState, limit:Number(e.dataset.limitBytes),
  account:e.closest('tr')?.querySelector('td')?.textContent.trim()})),
  headers:[...document.querySelectorAll('th')].map(e=>e.textContent),
  scripts:[...performance.getEntriesByType('resource')].map(e=>e.name),
  repositories:[...document.querySelectorAll('.js-bytes')].map(e=>e.textContent),
  body:document.body.innerText};
"""
result = call(8, "WebDriver:ExecuteScript", {"script": script, "args": []})
value = result[3]["value"]
if not value["resources"] or any(r["state"] != "finite" or r["limit"] <= 0 for r in value["resources"]):
    raise AssertionError(value)
if "no applicable limit" in value["body"] or " / 0 B" in value["body"]:
    raise AssertionError(value["body"])
for account, limits in expected.items():
    got = [r["limit"] for r in value["resources"] if r["account"] == account]
    if got != limits["limits"]:
        raise AssertionError({account: (got, limits["limits"]), "resources": value["resources"]})
    texts = [r["text"] for r in value["resources"] if r["account"] == account]
    if texts != limits["texts"]:
        raise AssertionError({account: (texts, limits["texts"]), "resources": value["resources"]})
if "Egress" not in value["headers"] or "Egress (30d)" not in value["headers"]:
    raise AssertionError(value["headers"])
if not value["repositories"] or any(" / " in text for text in value["repositories"]):
    raise AssertionError({"repository_rows": value["repositories"]})
if not any(x.endswith("/static/ops.js") for x in value["scripts"]):
    raise AssertionError(value["scripts"])
logs = call(9, "WebDriver:GetLogs", {"type": "browser"})
if len(logs) > 3 and isinstance(logs[3], dict) and "error" in logs[3]:
    raise AssertionError({"browser_log_command": logs[3]})
if len(logs) > 3 and isinstance(logs[3], dict) and any(entry.get("level") in ("SEVERE", "ERROR") for entry in logs[3].get("value", [])):
    raise AssertionError({"browser_logs": logs[3]["value"]})
call(10, "WebDriver:DeleteSession", {})
print("REAL LOCAL CANDIDATE FIREFOX ESR TEST: PASS")
