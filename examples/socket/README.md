# A socket-activated service

systemd listens on the port (`hello-hello.socket`) and hands the socket to
hello.py. A restart replaces the program but not the socket, so
connections made while it restarts wait in the socket's backlog instead
of being refused. It needs python3.

```sh
cd examples/socket
systemd-compose up
curl -s localhost:8090
while curl -s localhost:8090; do sleep 0.2; done &   # every request answered...
systemd-compose restart hello                        # ...through the restart
kill %1
systemd-compose down
```
