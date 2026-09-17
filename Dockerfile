# Build frontend
FROM node:20-alpine AS build-frontend
WORKDIR /app
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# Build backend
FROM golang:1.27-alpine AS build-backend
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o server main.go

# Final image
FROM alpine:latest
WORKDIR /app
COPY --from=build-backend /app/server .
COPY --from=build-frontend /app/dist ./frontend/dist
EXPOSE 8086
CMD ["./server"]
