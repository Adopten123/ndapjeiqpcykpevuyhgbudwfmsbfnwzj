FROM node:22.19.0-bookworm-slim AS build
WORKDIR /frontend
COPY app/frontend/package.json app/frontend/package-lock.json ./
RUN npm ci
COPY app/frontend/ ./
RUN npm run build

FROM nginx:1.28.0-alpine
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /frontend/dist/ /usr/share/nginx/html/
EXPOSE 80
