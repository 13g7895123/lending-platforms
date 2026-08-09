FROM node:22-alpine AS dependencies

WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci

FROM dependencies AS build
COPY frontend/ ./
RUN npm run build

FROM node:22-alpine

WORKDIR /app
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=3000
COPY --from=build /app/.output ./.output

EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
