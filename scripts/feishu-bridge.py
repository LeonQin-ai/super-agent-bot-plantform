#!/usr/bin/env python3
"""Forward authorized Feishu WebSocket text events to the local control plane."""

import hashlib
import hmac
import json
import logging
import os
from pathlib import Path
import re
import secrets
import time
import urllib.request

import lark_oapi as lark


def message_text(message):
    content = json.loads(message.content)
    if message.message_type == "text":
        return content.get("text", "").strip()
    if message.message_type != "post":
        return ""
    # Feishu can wrap inline-code text in a rich-text message.
    rows = content.get("content")
    if not isinstance(rows, list):
        return ""
    if any(item.get("tag") != "text" for row in rows for item in row):
        return ""
    return "\n".join("".join(item.get("text", "") for item in row) for row in rows).strip()


def main():
    token = os.environ["FEISHU_VERIFICATION_TOKEN"]
    key = os.environ["FEISHU_ENCRYPT_KEY"]
    allowed = set(os.environ.get("FEISHU_ALLOWED_OPEN_IDS", "").split(","))
    owner = Path(os.environ["FEISHU_ALLOWED_OPEN_ID_FILE"])
    enrollment = owner.with_name("enrollment")
    listen = os.environ["LIGHT_AGENT_LISTEN_ADDR"]
    if not listen.startswith("127.0.0.1:"):
        raise ValueError("Feishu bridge requires an IPv4 loopback control plane")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def authorize(open_id, text):
        if open_id in allowed:
            return True
        if owner.exists():
            return owner.read_text().strip() == open_id
        if not enrollment.exists() or time.time() - enrollment.stat().st_mtime > 86400:
            return False
        marker = enrollment.read_text().strip()
        if not marker or not hmac.compare_digest(marker.encode(), text.encode()):
            return False
        try:
            with owner.open("x") as target:
                target.write(open_id + "\n")
        except FileExistsError:
            return owner.read_text().strip() == open_id
        enrollment.unlink(missing_ok=True)
        print("Feishu owner enrolled", flush=True)
        return True

    def receive(data):
        message = data.event.message
        sender = data.event.sender
        open_id = sender.sender_id.open_id or ""
        if message.chat_type != "p2p" or sender.sender_type != "user" or not re.fullmatch(r"ou_[A-Za-z0-9_-]+", open_id):
            return
        text = message_text(message)
        if not text or len(text.encode()) > 16 << 10 or not authorize(open_id, text):
            return
        if text.startswith("sabp-bind-"):
            text = "请用一句中文确认机器人已连接，并说明可帮我处理什么任务。"
        body = json.dumps({
            "schema": "2.0",
            "header": {"event_id": data.header.event_id, "event_type": "im.message.receive_v1", "token": token},
            "event": {
                "sender": {"sender_id": {"open_id": open_id}},
                "message": {"message_id": message.message_id, "chat_type": "p2p",
                            "chat_id": message.chat_id, "content": json.dumps({"text": text})},
            },
        }, ensure_ascii=False).encode()
        stamp, nonce = str(int(time.time())), secrets.token_hex(16)
        signature = hashlib.sha256((stamp + nonce + key).encode() + body).hexdigest()
        request = urllib.request.Request("http://" + listen + "/webhooks/feishu", body, {
            "Content-Type": "application/json",
            "X-Lark-Request-Timestamp": stamp,
            "X-Lark-Request-Nonce": nonce,
            "X-Lark-Signature": signature,
        })
        try:
            with opener.open(request, timeout=2) as response:
                response.read()
        except OSError:
            print("Feishu event could not be queued", flush=True)
            raise
        print("Feishu direct message queued", flush=True)

    class Client(lark.ws.Client):
        async def _connect(self):
            await super()._connect()
            print("Feishu WebSocket connected", flush=True)

    # SDK diagnostics can include signed connection URLs and full events.
    logging.disable(logging.CRITICAL)
    handler = lark.EventDispatcherHandler.builder("", "").register_p2_im_message_receive_v1(receive).build()
    try:
        Client(os.environ["FEISHU_APP_ID"], os.environ["FEISHU_APP_SECRET"], event_handler=handler).start()
    except Exception as error:
        print("Feishu bridge exited: " + type(error).__name__, flush=True)
        raise SystemExit(1) from None


if __name__ == "__main__":
    os.umask(0o077)
    main()
