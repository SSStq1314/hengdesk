  root /usr/share/nginx/html;
  client_max_body_size 100m;

  location /api/ {
    proxy_pass http://server:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    # SSE 流式输出
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
  }
  location = /healthz { proxy_pass http://server:8080/healthz; }
  location /assets/ { expires 30d; add_header Cache-Control "public, immutable"; try_files $uri =404; }
  location / { try_files $uri $uri/ /index.html; }
