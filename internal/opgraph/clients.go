package opgraph

import "strings"

// knownClient names what a well-known client library talks to. Network
// stores and brokers are system-wide parties; embedded stores are per unit.
// Extending this table is the extension point for new ecosystems.
type knownClientEntry struct {
	match  string
	kind   Kind
	system string
	shared bool
}

func client(match string, kind Kind, system string) knownClientEntry {
	return knownClientEntry{match: match, kind: kind, system: system, shared: system != "sqlite"}
}

// Go import path prefixes.
var goClients = []knownClientEntry{
	client("github.com/ncruces/go-sqlite3", Store, "sqlite"), client("modernc.org/sqlite", Store, "sqlite"),
	client("github.com/mattn/go-sqlite3", Store, "sqlite"), client("github.com/lib/pq", Store, "postgresql"),
	client("github.com/jackc/pgx", Store, "postgresql"), client("github.com/go-sql-driver/mysql", Store, "mysql"),
	client("github.com/redis/go-redis", Store, "redis"), client("github.com/go-redis/redis", Store, "redis"),
	client("github.com/gomodule/redigo", Store, "redis"), client("go.mongodb.org/mongo-driver", Store, "mongodb"),
	client("github.com/segmentio/kafka-go", Queue, "kafka"), client("github.com/IBM/sarama", Queue, "kafka"),
	client("github.com/confluentinc/confluent-kafka-go", Queue, "kafka"), client("github.com/nats-io/nats.go", Queue, "nats"),
	client("github.com/rabbitmq/amqp091-go", Queue, "rabbitmq"), client("github.com/streadway/amqp", Queue, "rabbitmq"),
	client("github.com/aws/aws-sdk-go-v2/service/s3", Store, "s3"), client("github.com/aws/aws-sdk-go-v2/service/sqs", Queue, "sqs"),
	client("github.com/aws/aws-sdk-go-v2/service/dynamodb", Store, "dynamodb"), client("cloud.google.com/go/pubsub", Queue, "pubsub"),
	client("cloud.google.com/go/storage", Store, "gcs"), client("cloud.google.com/go/firestore", Store, "firestore"),
	client("github.com/stripe/stripe-go", External, "stripe"), client("github.com/getsentry/sentry-go", External, "sentry"),
}

// Package names in JavaScript, Python, Ruby, Rust and JVM manifests.
var manifestClients = []knownClientEntry{
	client("pg", Store, "postgresql"), client("postgres", Store, "postgresql"), client("@neondatabase/serverless", Store, "postgresql"),
	client("@vercel/postgres", Store, "postgresql"), client("psycopg2", Store, "postgresql"), client("psycopg2-binary", Store, "postgresql"),
	client("psycopg", Store, "postgresql"), client("asyncpg", Store, "postgresql"), client("tokio-postgres", Store, "postgresql"),
	client("postgresql", Store, "postgresql"), client("mysql2", Store, "mysql"), client("pymysql", Store, "mysql"),
	client("mysqlclient", Store, "mysql"), client("mysql-connector-j", Store, "mysql"), client("mongodb", Store, "mongodb"),
	client("mongoose", Store, "mongodb"), client("pymongo", Store, "mongodb"), client("redis", Store, "redis"), client("ioredis", Store, "redis"),
	client("@upstash/redis", Store, "redis"), client("@vercel/kv", Store, "redis"), client("spring-boot-starter-data-redis", Store, "redis"),
	client("better-sqlite3", Store, "sqlite"), client("sqlite3", Store, "sqlite"), client("rusqlite", Store, "sqlite"),
	client("@prisma/client", Store, "sql"), client("drizzle-orm", Store, "sql"), client("sqlx", Store, "sql"), client("sqlalchemy", Store, "sql"),
	client("kafkajs", Queue, "kafka"), client("kafka-python", Queue, "kafka"), client("confluent-kafka", Queue, "kafka"),
	client("rdkafka", Queue, "kafka"), client("spring-kafka", Queue, "kafka"), client("bullmq", Queue, "bullmq"), client("bull", Queue, "bull"),
	client("amqplib", Queue, "rabbitmq"), client("lapin", Queue, "rabbitmq"), client("spring-boot-starter-amqp", Queue, "rabbitmq"),
	client("celery", Queue, "celery"), client("sidekiq", Queue, "sidekiq"), client("resque", Queue, "resque"),
	client("@aws-sdk/client-s3", Store, "s3"), client("@aws-sdk/client-sqs", Queue, "sqs"), client("@aws-sdk/client-dynamodb", Store, "dynamodb"),
	client("boto3", External, "aws"), client("stripe", External, "stripe"), client("@sentry/nextjs", External, "sentry"),
	client("@sentry/node", External, "sentry"), client("@sentry/browser", External, "sentry"), client("@sentry/react", External, "sentry"),
	client("sentry-sdk", External, "sentry"), client("sentry-ruby", External, "sentry"), client("posthog-js", External, "posthog"),
	client("posthog-node", External, "posthog"), client("posthog", External, "posthog"),
}

func knownClient(name string, table []knownClientEntry) *knownClientEntry {
	for index, entry := range table {
		if name == entry.match || strings.HasPrefix(entry.match, "github.com/") && strings.HasPrefix(name, entry.match) ||
			strings.Contains(entry.match, ".") && strings.HasPrefix(name, entry.match+"/") {
			return &table[index]
		}
	}
	return nil
}

func (run *inference) client(entry knownClientEntry, found owner, at Location, detail string) {
	label := entry.system
	edge := Uses
	if entry.kind == External {
		edge = Calls
	}
	key := "store:" + entry.system
	switch entry.kind {
	case Queue:
		key = "queue:" + entry.system
	case External:
		key = "service:" + entry.system
	}
	selector := map[Kind]string{Store: "db.system.name", Queue: "messaging.system", External: "peer.service"}[entry.kind]
	run.find(finding{owner: found, kind: entry.kind, key: key, label: label, shared: entry.shared, edge: edge,
		selectors: map[string]string{selector: entry.system},
		evidence:  static("manifest", detail, Inferred), at: at})
}
