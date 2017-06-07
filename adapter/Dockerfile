FROM alpine

RUN apk add --no-cache tzdata libc6-compat && \
       ln -s /lib /lib64 && \
       cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
       echo "Asia/Shanghai" >  /etc/timezone && \
       date && apk del --no-cache tzdata
       
RUN sed -i -r 's/nofiles/rain/' /etc/group && \
    adduser -u 200 -D -S -G rain rain
    
RUN apk add --no-cache wget curl netcat-openbsd iputils net-tools bash su-exec && \
    sed -i -e "s/bin\/ash/bin\/bash/" /etc/passwd

ADD grproxy /grproxy

COPY docker-entrypoint.sh /tmp

RUN chmod +x /tmp/*

WORKDIR /tmp

ENTRYPOINT ["/tmp/docker-entrypoint.sh"]
