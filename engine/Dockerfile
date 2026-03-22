FROM golang:1.22 AS build
WORKDIR /src
 
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/flowsentinel-engine ./main.go

FROM scratch
USER 65532:65532
COPY --from=build /out/flowsentinel-engine /flowsentinel-engine
EXPOSE 8080
ENTRYPOINT ["/flowsentinel-engine"]

