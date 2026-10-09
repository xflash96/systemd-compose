"""The api's healthcheck: exits 0 when the URL answers."""
import sys
import urllib.request

try:
    urllib.request.urlopen(sys.argv[1], timeout=2)
except OSError as e:
    sys.exit(f"no answer: {e}")
