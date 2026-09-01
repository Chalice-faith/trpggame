FROM node:22-alpine

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY database ./database

CMD ["npm", "run", "db:migrate"]
