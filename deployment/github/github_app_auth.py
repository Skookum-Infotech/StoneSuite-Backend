#!/usr/bin/env python3

import os
import time
import jwt
import requests
import subprocess
import tempfile
import sys
import base64


def fail(message):
    print(f"ERROR: {message}")
    sys.exit(1)


def get_env(name):
    value = os.getenv(name)
    if not value:
        fail(f"Missing environment variable: {name}")
    return value


print("StoneSuite GHCR Authentication")
print("--------------------------------")

APP_ID = get_env("GITHUB_APP_ID")
INSTALLATION_ID = get_env("GITHUB_INSTALLATION_ID")
# PRIVATE_KEY = get_env("GITHUB_PRIVATE_KEY")
PRIVATE_KEY = base64.b64decode(os.environ["GITHUB_PRIVATE_KEY_B64"])

now = int(time.time())

payload = {
    "iat": now - 60,
    "exp": now + 600,
    "iss": APP_ID,
}

jwt_token = jwt.encode(
    payload,
    private_key,
    algorithm="RS256",
)

print("Requesting installation access token...")

headers = {
    "Authorization": f"Bearer {jwt_token}",
    "Accept": "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
}

response = requests.post(
    f"https://api.github.com/app/installations/{INSTALLATION_ID}/access_tokens",
    headers=headers,
)

if response.status_code != 201:
    print(response.text)
    fail("Unable to create installation token.")

installation_token = response.json()["token"]

print("Logging into GHCR...")

docker = subprocess.run(
    [
        "docker",
        "login",
        "ghcr.io",
        "-u",
        "x-access-token",
        "--password-stdin",
    ],
    input=installation_token.encode(),
)

if docker.returncode != 0:
    fail("Docker login failed.")

print("")
print("SUCCESS")
print("Authenticated to GHCR.")
