import socketserver, sys, time
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 49888
BODY = open('/root/big.bin', 'rb').read()
HEAD = b'HTTP/1.0 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n' % len(BODY)

def log(*a):
    print(time.strftime('%H:%M:%S'), *a, flush=True)

class H(socketserver.StreamRequestHandler):
    def handle(self):
        try:
            first = self.rfile.readline()
            method = first.split(b' ')[0] if b' ' in first else b'?'
            n = 0
            while True:
                line = self.rfile.readline()
                if not line or line in (b'\r\n', b'\n'):
                    break
                if line.lower().startswith(b'content-length:'):
                    n = int(line.split(b':')[1].strip())
            if n > 0:
                # upload target: drain body, then ack
                left, got = n, 0
                while left > 0:
                    chunk = self.rfile.read(min(left, 65536))
                    if not chunk:
                        break
                    left -= len(chunk)
                    got += len(chunk)
                log('up-done', method, self.client_address[1], got)
                self.wfile.write(b'HTTP/1.0 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok')
                return
            self.wfile.write(HEAD)
            self.wfile.write(BODY)
            log('sent-ok', self.client_address[1])
        except Exception as e:
            log('send-err', self.client_address[1], repr(e))

class S(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

S(('0.0.0.0', PORT), H).serve_forever()
