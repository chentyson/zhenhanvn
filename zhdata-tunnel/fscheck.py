import socket
s = socket.create_connection(('127.0.0.1', 8000), timeout=5)
s.settimeout(5)
data = b''
try:
    while len(data) < 40:
        chunk = s.recv(4096)
        if not chunk:
            break
        data += chunk
except Exception as e:
    print('ERR', e)
print('GOT', len(data), repr(data[:40]))
s.close()
