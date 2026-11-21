# An image with typical mistakes for the CLI smoke test: a build cache and a
# credentials file deleted in a later layer, so both still ship in the image.
FROM nginx:1.27-alpine
COPY npmrc /root/.npmrc
RUN mkdir -p /app && head -c 3000000 /dev/urandom > /app/cache.tar
RUN printf '<h1>smoke</h1>\n' > /usr/share/nginx/html/index.html
RUN rm /app/cache.tar /root/.npmrc
