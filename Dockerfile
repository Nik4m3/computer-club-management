# multi-stage
FROM golang:1.23 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/computer_club ./


FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=builder /bin/computer_club /bin/computer_club
COPY static ./static
ENV HTTP_ADDR=:8080
ENV DB_DSN=postgres://computer_club:computer_club@db:5432/computer_club?sslmode=disable
EXPOSE 8080
ENTRYPOINT ["/bin/computer_club"]