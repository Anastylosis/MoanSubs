# End-user documentation (docs.moansubs.org): mkdocs build, served by nginx.
# Built from the repository root so mkdocs.yml and docs/user are in context.
FROM squidfunk/mkdocs-material:9 AS build
WORKDIR /docs
COPY mkdocs.yml ./
COPY docs/user ./docs/user
RUN mkdocs build --strict --site-dir /site

FROM nginx:1.27-alpine
COPY docs/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /site /usr/share/nginx/html
