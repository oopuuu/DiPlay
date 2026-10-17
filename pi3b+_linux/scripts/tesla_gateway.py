import socket, threading, http.server

def dns():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(('0.0.0.0', 53))
    while True:
        try:
            d, a = s.recvfrom(512)
            s.sendto(d[:2] + bytes.fromhex('8180') + d[4:6] + bytes.fromhex('000100000000') + d[12:] + bytes.fromhex('c00c000100010000003c000464600001'), a)
        except: pass

threading.Thread(target=dns, daemon=True).start()

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        h = self.headers.get('Host', '')
        if 'connman' in h:
            self.send_response(200)
            self.send_header('X-ConnMan-Status', 'online')
            self.send_header('Content-Type', 'text/html')
            self.end_headers()
            self.wfile.write(b'<html><body></body></html>')
        elif 'baidu' in h or 'qq.com' in h or 'tesla' in h:
            self.send_response(200)
            self.send_header('Content-Type', 'text/html')
            self.end_headers()
            self.wfile.write(b'<html><body>online</body></html>')
        elif '204' in self.path:
            self.send_response(204)
            self.end_headers()
        else:
            self.send_response(302)
            self.send_header('Location', 'http://100.96.0.1:8088' + self.path)
            self.end_headers()
    def do_HEAD(self): self.do_GET()
    def log_message(self, *a): pass

http.server.HTTPServer(('0.0.0.0', 80), H).serve_forever()
