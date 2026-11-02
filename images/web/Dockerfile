ARG NODE_IMAGE=docker.io/library/node@sha256:a5dbaa51d1be96b3415b993320893038c7240d6dd901166ee597f3b1cc3259d1
ARG NGINX_IMAGE=docker.io/library/nginx@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10

FROM ${NODE_IMAGE} AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM ${NGINX_IMAGE}
COPY images/web/nginx.conf /etc/nginx/nginx.conf
COPY --from=web-build /src/web/dist /usr/share/nginx/html
USER 101:101
EXPOSE 8080
ENTRYPOINT ["nginx", "-g", "daemon off;"]
