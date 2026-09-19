#!/usr/bin/env python3
"""Read-only smoke check of the installed manager, using its local credentials."""

import base64
import json
import os
import shlex
import urllib.parse
import urllib.request


def main():
    values = {}
    with open(os.environ.get("CONAN_MANAGER_ENV_FILE", "/etc/conan-manager/conan-manager.env")) as source:
        for line in source:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            key, value = line.split("=", 1)
            tokens = shlex.split(value, comments=False)
            values[key] = " ".join(tokens)

    address = values.get("CONAN_MANAGER_LISTEN", "127.0.0.1:8080")
    base = "http://" + address
    headers = {}
    username = values.get("CONAN_MANAGER_ADMIN_USERNAME", "")
    password = values.get("CONAN_MANAGER_ADMIN_PASSWORD", "")
    if username and password:
        token = base64.b64encode(f"{username}:{password}".encode()).decode()
        headers["Authorization"] = "Basic " + token

    def get(path):
        request = urllib.request.Request(base + path, headers=headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            assert response.status == 200
            return json.load(response)

    assert get("/healthz")["status"] == "ok"
    assert get("/api/v1/info")["service"] == "conan-manager"
    servers = get("/api/v1/servers") or []
    for server in servers:
        detail = get("/api/v1/servers/" + urllib.parse.quote(server["id"], safe=""))
        assert "state" in detail["lifecycle"]
        assert "availability" in detail["health"]
        print(json.dumps({"id": server["id"], "lifecycle": detail["lifecycle"],
                          "availability": detail["health"]["availability"]}))
    print(f"API smoke check passed: health, info, {len(servers)} server details")


if __name__ == "__main__":
    main()
