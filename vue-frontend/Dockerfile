ARG NODE_IMAGE=node:24-alpine
FROM ${NODE_IMAGE} AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN VITE_API_BASE_URL=/ VITE_WS_URL= VITE_IM_WS_URL= npm run build
FROM nginx:alpine
COPY --from=build /app/dist /usr/share/nginx/html
