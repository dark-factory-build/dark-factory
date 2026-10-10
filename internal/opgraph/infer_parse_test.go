package opgraph

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/opgraph/treesitter"
)

// languageFixtures are small, realistic apps: one repository per framework.
func languageFixtures() []Repository {
	return []Repository{
		{ID: "node", Name: "node", Files: map[string][]byte{
			"package.json": []byte(`{"name":"api","scripts":{"start":"node dist/main.js"},"dependencies":{"express":"4","@nestjs/common":"10","pg":"8","kafkajs":"2","bullmq":"5"}}`),
			"src/main.ts": []byte(`import express from 'express';
import { Pool } from 'pg';
import './users.controller';
import { publish } from './queues.js';
const app = express();
const API = '/api';
const v1 = express.Router();
v1.get(` + "`${API}/health`" + `, (req, res) => res.send('ok'));
app.use('/v1', v1);
app.post(API + '/checkout', create);
app.set('view engine', 'pug');
const PAYMENTS_URL = 'https://payments.vendor.io';
const docs = "https://docs.only-a-link.io";
// fetch('https://commented.io')
async function create() { await fetch(` + "`${PAYMENTS_URL}/charge`" + `, { method: 'POST' }); }
app.listen(3000);
`),
			"src/users.controller.ts": []byte(`import { Controller, Get, Post } from '@nestjs/common';
@Controller('users')
export class UsersController {
  @Get(':id') find() {}
  @Post() create() {}
}
`),
			"src/scripts/seed.ts": []byte("import express from 'express';\nconst app = express();\napp.get('/never', h);\n"),
			"src/queues.ts": []byte(`import { Queue, Worker } from 'bullmq';
import { Kafka } from 'kafkajs';
const emails = new Queue('emails');
new Worker('emails', async () => {});
export async function publish(producer) { await producer.send({ topic: 'orders', messages: [] }); }
`),
		}},
		{ID: "python", Name: "python", Files: map[string][]byte{
			"Procfile":         []byte("web: uvicorn main:app\n"),
			"requirements.txt": []byte("fastapi\nhttpx\n"),
			"main.py": []byte(`from fastapi import FastAPI, APIRouter
import httpx
import redis
app = FastAPI()
items = APIRouter(prefix="/items")
BILLING = "https://billing.vendor.io"

@items.get("/{item_id}")
async def read(item_id: int):
    async with httpx.AsyncClient(base_url=BILLING) as client:
        return await client.get("/v1/invoices")

@app.post("/orders")
def create():
    pass

app.include_router(items, prefix="/api")
`),
			"tasks.py": []byte("from celery import shared_task\n\n@shared_task\ndef send_receipt(order):\n    pass\n"),
			"shop/urls.py": []byte(`from django.urls import path, include
urlpatterns = [path("shop/", include("shop.api.urls"))]
`),
			"shop/api/urls.py": []byte(`from django.urls import path
urlpatterns = [path("carts/<int:pk>/", view)]
`),
		}},
		{ID: "ruby", Name: "ruby", Files: map[string][]byte{
			"config.ru": []byte("run Rails.application"),
			"config/routes.rb": []byte(`Rails.application.routes.draw do
  root 'home#index'
  namespace :api do
    resources :orders, only: [:index, :show] do
      resources :items, only: :create
      member do
        post :refund
      end
    end
    resources :categories, only: [] do
      get :summary
    end
    post 'webhooks/stripe' => 'webhooks#stripe'
  end
end
`),
			"app/jobs/application_job.rb":  []byte("class ApplicationJob < ActiveJob::Base\nend\n"),
			"app/jobs/receipt_job.rb":      []byte("class ReceiptJob < ApplicationJob\n  def perform; end\nend\n"),
			"app/workers/hard_worker.rb":   []byte("class HardWorker\n  include Sidekiq::Job\n  def perform; end\nend\n"),
			"app/services/payment_gate.rb": []byte("API_URL = \"https://gateway.vendor.io\"\nNet::HTTP.post(URI(\"#{API_URL}/charge\"), body)\n"),
		}},
		{ID: "jvm", Name: "jvm", Files: map[string][]byte{
			"pom.xml": []byte("<project><artifactId>shop</artifactId><dependencies><dependency><artifactId>spring-kafka</artifactId></dependency></dependencies></project>"),
			"src/main/java/com/x/App.java": []byte(`package com.x;
import org.springframework.boot.autoconfigure.SpringBootApplication;
@SpringBootApplication
public class App { public static void main(String[] args) {} }
`),
			"src/main/java/com/x/OrderController.java": []byte(`package com.x;
import org.springframework.web.bind.annotation.*;
@Controller
@RequestMapping("/api/orders")
public class OrderController {
  private static final String BY_ID = "/{id}";
  @GetMapping(BY_ID) public Order get(long id) { return rest.getForObject("https://inventory.vendor.io/stock/" + id, Order.class); }
  @PostMapping public Order create() { return null; }
  @RequestMapping(value = "/sync", method = RequestMethod.PUT) void sync() {}
  @Scheduled(cron = "0 0 * * * *") void nightly() {}
  @KafkaListener(topics = {"orders", "refunds"}) void on(String m) { kafkaTemplate.send("audit", m); }
}
`),
			"src/main/kotlin/Routes.kt": []byte(`package com.x

import io.ktor.server.routing.*

const val ACCOUNTS = "https://accounts.vendor.io"
fun Application.module() { routing { route("/v2") { get("/status") { call.respondText(client.get("$ACCOUNTS/me")) } } } }
`),
			"src/main/kotlin/KtController.kt": []byte(`package com.x

import org.springframework.web.bind.annotation.*

@RestController
@RequestMapping("/kt")
class KtController {
    @GetMapping("/ping")
    fun ping() = "pong"
}
`),
		}},
		{ID: "rust", Name: "rust", Files: map[string][]byte{
			"Cargo.toml": []byte("[package]\nname = \"edge\"\n[dependencies]\naxum = \"0.7\"\n"),
			"src/main.rs": []byte(`use axum::{routing::{get, post}, Router};
use sqlx::PgPool;
const UPSTREAM: &str = "https://upstream.vendor.io";
fn api() -> Router { Router::new().route("/items/:id", get(show).delete(remove)) }
#[tokio::main]
async fn main() { let app = Router::new().route("/health", get(health)).route("/ready", get(ready)).nest("/api", api()); }
async fn show() { reqwest::get(format!("{}/items", UPSTREAM)).await; }
async fn tick() { let mut every = tokio::time::interval(Duration::from_secs(5)); }
`),
			"src/legacy.rs": []byte("use actix_web::get;\n#[get(\"/hello/{name}\")]\nasync fn hello() -> String { String::new() }\n"),
		}},
	}
}

func TestInferLanguages(t *testing.T) {
	graph, err := Infer("system", languageFixtures(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byID, byLabel := map[string]Node{}, map[string][]Node{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
		byLabel[node.Label] = append(byLabel[node.Label], node)
	}
	routes := []struct{ unit, label, method, route, confidence string }{
		{"api", "GET /v1/api/health", "GET", "/v1/api/health", Declared},
		{"api", "POST /api/checkout", "POST", "/api/checkout", Declared},
		{"api", "GET /users/:id", "GET", "/users/:id", Declared},
		{"api", "POST /users", "POST", "/users", Declared},
		{"web", "GET /api/items/{item_id}", "GET", "/api/items/{item_id}", Declared},
		{"web", "POST /orders", "POST", "/orders", Declared},
		{"web", "/shop/carts/<int:pk>/", "", "/shop/carts/<int:pk>/", Declared},
		{"ruby", "GET /", "GET", "/", Declared},
		{"ruby", "GET /api/orders", "GET", "/api/orders", Declared},
		{"ruby", "GET /api/orders/:id", "GET", "/api/orders/:id", Declared},
		{"ruby", "POST /api/orders/:order_id/items", "POST", "/api/orders/:order_id/items", Declared},
		{"ruby", "POST /api/webhooks/stripe", "POST", "/api/webhooks/stripe", Declared},
		{"ruby", "POST /api/orders/:id/refund", "POST", "/api/orders/:id/refund", Declared},
		{"ruby", "GET /api/categories/:category_id/summary", "GET", "/api/categories/:category_id/summary", Declared},
		{"jvm", "GET /api/orders/{id}", "GET", "/api/orders/{id}", Declared},
		{"jvm", "POST /api/orders", "POST", "/api/orders", Declared},
		{"jvm", "PUT /api/orders/sync", "PUT", "/api/orders/sync", Declared},
		{"jvm", "GET /v2/status", "GET", "/v2/status", Declared},
		{"jvm", "GET /kt/ping", "GET", "/kt/ping", Declared},
		{"edge", "GET /health", "GET", "/health", Declared},
		{"edge", "GET /ready", "GET", "/ready", Declared}, // a chain's calls share a start
		{"edge", "GET /api/items/:id", "GET", "/api/items/:id", Declared},
		{"edge", "DELETE /api/items/:id", "DELETE", "/api/items/:id", Declared},
		{"edge", "GET /hello/{name}", "GET", "/hello/{name}", Declared},
	}
	for _, want := range routes {
		nodes := byLabel[want.label]
		if len(nodes) != 1 {
			t.Errorf("%q: %d nodes, want 1", want.label, len(nodes))
			continue
		}
		node := nodes[0]
		if byID[node.Unit].Label != want.unit || node.Trigger != "request" || node.Selectors["http.route"] != want.route ||
			node.Selectors["http.request.method"] != want.method || node.Evidence[0].Confidence != want.confidence {
			t.Errorf("%q = %+v in %q", want.label, node, byID[node.Unit].Label)
		}
	}
	for _, want := range [][3]string{
		{"api", "calls", "payments.vendor.io"},
		{"api", "uses", "postgresql"},
		{"api", "publishes", "emails"},
		{"emails", "consumes", "api"},
		{"api", "publishes", "orders"},
		{"web", "calls", "billing.vendor.io"},
		{"web", "uses", "redis"},
		{"ruby", "calls", "gateway.vendor.io"},
		{"ruby", "runs", "ReceiptJob"},
		{"ruby", "runs", "HardWorker"},
		{"jvm", "calls", "inventory.vendor.io"},
		{"jvm", "calls", "accounts.vendor.io"},
		{"orders", "consumes", "jvm"},
		{"refunds", "consumes", "jvm"},
		{"jvm", "publishes", "audit"},
		{"nightly", "handles", "jvm"},
		{"edge", "calls", "upstream.vendor.io"},
		{"edge", "runs", "tick"},
		{"edge", "uses", "sql"},
	} {
		if !edge(graph, byID, want[0], want[1], want[2]) {
			t.Errorf("missing edge %v", want)
		}
	}
	if task := one(t, byLabel, "send_receipt"); task.Kind != Job || task.Evidence[0].Confidence != Declared {
		t.Errorf("celery task = %+v", task)
	}
	if queue := one(t, byLabel, "orders"); queue.Selectors["messaging.destination.name"] != "orders" || queue.Kind != Queue {
		t.Errorf("queue = %+v", queue)
	}
	// The node package starts dist/main.js: built from src/main.ts, which
	// imports what runs. A script it never imports is no route of the API.
	for _, absent := range []string{"docs.only-a-link.io", "commented.io", "GET view engine", "GET /never", "ApplicationJob"} {
		if len(byLabel[absent]) != 0 {
			t.Errorf("%q became a node", absent)
		}
	}
}

// Identical input gives an identical graph: no map order, timing or parser
// state leaks into it.
func TestParsedInferenceIsDeterministic(t *testing.T) {
	encode := func(repositories []Repository) string {
		graph, err := Infer("system", repositories, nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(graph)
		return string(encoded)
	}
	first := encode(languageFixtures())
	for attempt := 0; attempt < 5; attempt++ {
		if encode(languageFixtures()) != first {
			t.Fatal("inference changed between identical runs")
		}
	}
}

// A malformed, an oversized or a pathological file yields no evidence of its
// own and takes nothing else down with it.
func TestBadFilesDegradeAlone(t *testing.T) {
	huge := "app.get('/huge', h)\n" + strings.Repeat("x = 1;\n", maxParseBytes/7+1)
	repositories := []Repository{{ID: "r", Name: "r", Files: map[string][]byte{
		"package.json":        []byte(`{"name":"r","dependencies":{"express":"4"}}`),
		"src/a.ts":            []byte("app.get('/kept', h)\n"),
		"src/broken.ts":       []byte("app.get('/broken', h\n}}}} ((( \x00\xff\xfe class {{{ <<<\n"),
		"src/huge.ts":         []byte(huge),
		"src/nested.ts":       []byte(strings.Repeat("a(", 100_000)),
		"src/overflow.ts":     []byte(strings.Repeat("{a:", 80_000)), // overflows the wasm stack: a trap
		"src/slow.ts":         []byte(strings.Repeat("[", 250_000)),  // quadratic to query: the deadline
		"src/z.ts":            []byte("app.post('/after', h)\n"),
		"src/binary.py":       []byte("\xff\xfe\x00\x01" + strings.Repeat("\x00", 4096)),
		"src/unicode.ts":      []byte("const s = '日本語😀';\napp.put('/unicode', h)\n"),
		"src/unterminated.rb": []byte("get \"/x\n"),
	}}}
	graph, err := Infer("system", repositories, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, node := range graph.Nodes {
		labels[node.Label] = true
	}
	for _, want := range []string{"GET /kept", "POST /after", "PUT /unicode"} {
		if !labels[want] {
			t.Errorf("%q missing: a bad file took a good one down", want)
		}
	}
	if labels["GET /huge"] {
		t.Error("a file past the parse bound contributed evidence")
	}
}

func TestQueriesCompile(t *testing.T) {
	for language, query := range queries {
		parser, err := treesitter.New(context.Background(), language)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.Query(query); err != nil {
			t.Errorf("%s: %v", language, err)
		}
		parser.Close()
	}
}

func TestStringResolution(t *testing.T) {
	for _, item := range []struct{ language, source, want string }{
		{"typescript", "const A = '/a'; const B = `${A}/b`; f(B + '/c')", "/a/b/c"},
		{"python", "A = '/a'\nB = f'{A}/b'\nf(B + \"/c\")", "/a/b/c"},
		{"ruby", "A = '/a'\nB = \"#{A}/b\"\nf(B + '/c')", "/a/b/c"},
		{"java", "class C { static final String A = \"/a\"; void m() { f(A + \"/b\" + \"/c\"); } }", "/a/b/c"},
		{"kotlin", "const val A = \"/a\"\nfun m() { f(\"$A/b\" + \"/c\") }", "/a/b/c"},
		{"rust", "const A: &str = \"/a\";\nfn m() { f(format!(\"{}/b/{}\", A, \"c\")); }", "/a/b/c"},
	} {
		parser, err := treesitter.New(context.Background(), item.language)
		if err != nil {
			t.Fatal(err)
		}
		query, _ := parser.Query(queries[item.language])
		found, err := parseFile(parser, query, item.language, "x", []byte(item.source))
		parser.Close()
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, call := range found.calls {
			if call.name == "f" && len(call.args) == 1 {
				got, _ = found.str(call.args[0])
			}
		}
		if got != item.want {
			t.Errorf("%s: resolved %q, want %q", item.language, got, item.want)
		}
	}
}
