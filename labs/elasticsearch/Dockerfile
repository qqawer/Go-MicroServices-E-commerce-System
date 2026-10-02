FROM docker.elastic.co/elasticsearch/elasticsearch:8.19.22@sha256:e98f9c3b09beb2fbb9eaf667d602df3f0e00bd3644138b8458dc17ba1a675595

# The plugin manager selects the plugin version matching this ES installation.
RUN bin/elasticsearch-plugin install --batch analysis-smartcn
