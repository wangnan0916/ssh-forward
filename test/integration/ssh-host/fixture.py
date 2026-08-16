#!/usr/bin/python3

import signal
import os
import socket
import threading

STOP = threading.Event()
LISTENERS = []


def stop(_signum, _frame):
    STOP.set()
    for listener in LISTENERS:
        listener.close()


def handle(connection):
    with connection:
        request = bytearray()
        while True:
            chunk = connection.recv(65536)
            if not chunk:
                break
            request.extend(chunk)
        connection.sendall(b"fixture:" + request)


def serve(listener):
    while not STOP.is_set():
        try:
            connection, _ = listener.accept()
        except OSError:
            if STOP.is_set():
                return
            raise
        threading.Thread(target=handle, args=(connection,), daemon=True).start()


def listen(family, host, port):
    listener = socket.socket(family, socket.SOCK_STREAM)
    if family == socket.AF_INET6:
        listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind((host, port))
    listener.listen()
    LISTENERS.append(listener)
    threading.Thread(target=serve, args=(listener,), daemon=True).start()


signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
port_v4 = int(os.environ.get("SSH_FORWARD_FIXTURE_PORT_V4", "38080"))
port_v6 = int(os.environ.get("SSH_FORWARD_FIXTURE_PORT_V6", "38081"))
listen(socket.AF_INET, "127.0.0.1", port_v4)
listen(socket.AF_INET6, "::1", port_v6)
STOP.wait()
