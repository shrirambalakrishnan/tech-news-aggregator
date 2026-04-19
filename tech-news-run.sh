#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export ANTHROPIC_API_KEY=$(/usr/bin/security find-generic-password -a "$(whoami)" -s "ANTHROPIC_API_KEY" -w)

if [ -z "$ANTHROPIC_API_KEY" ]; then
    echo "Error: ANTHROPIC_API_KEY not found in keychain. Run:"
    echo "  security add-generic-password -a \"\$(whoami)\" -s \"ANTHROPIC_API_KEY\" -w \"your-key\""
    exit 1
fi

if [ -x /usr/local/go/bin/go ]; then
    GO=/usr/local/go/bin/go
elif [ -x /opt/homebrew/bin/go ]; then
    GO=/opt/homebrew/bin/go
else
    GO=$(command -v go)
fi

cd "$SCRIPT_DIR"
$GO run .
