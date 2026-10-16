-- Apply before starting the traced outbox writer/dispatcher. Existing events stay NULL.
-- Only W3C traceparent/tracestate are persisted; no headers, body or credentials.
ALTER TABLE grpctest_orders_srv.order_event ADD COLUMN trace_context JSON NULL;
