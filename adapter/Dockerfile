FROM goodrainapps/alpine:3.6

RUN apk add --no-cache iputils net-tools && \
    sed -i -e "s/bin\/ash/bin\/bash/" /etc/passwd && \
    sed -i -r 's/nofiles/rain/' /etc/group && \
    adduser -u 200 -D -S -G rain rain

ADD grproxy /grproxy

COPY docker-entrypoint.sh /tmp

RUN chmod +x /tmp/*

WORKDIR /tmp
ENV RELEASE_DESC=__RELEASE_DESC__

ENTRYPOINT ["/tmp/docker-entrypoint.sh"]
