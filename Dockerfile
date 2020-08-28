# vim:set ft=dockerfile:
FROM alpine:edge as ca-certs

RUN apk --no-cache add ca-certificates

FROM scratch

COPY --from=ca-certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ADD waterme /bin/waterme

ENTRYPOINT ["/bin/waterme"]
