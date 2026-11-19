"use strict";
const el = id => document.getElementById(id);
let token = "", cursor = "", next = "", selected = "";

async function api(path, method = "GET", body) {
  const headers = {};
  if (token) headers.Authorization = "Bearer " + token;
  if (body) headers["Content-Type"] = "application/json";
  const response = await fetch("/v1/" + path, {method, headers, body: body && JSON.stringify(body)});
  if (response.status === 204) return;
  const data = await response.json();
  if (!response.ok) throw new Error(response.status === 401 ? "请输入有效的管理 Token。" : data.message || response.statusText);
  return data;
}

function report(text, error = false) {
  el("status").textContent = text;
  el("status").classList.toggle("error", error);
}

function node(tag, text) {
  const element = document.createElement(tag);
  element.textContent = text;
  return element;
}

const date = value => value ? new Date(value).toLocaleString() : "—";
const segment = encodeURIComponent;

async function refresh() {
  const [fleet, schedules] = await Promise.all([api("agents?after=" + segment(cursor)), api("schedules")]);
  el("runtime").textContent = fleet.runtime_configured ? "light-agent 执行已配置" : "待配置 light-agent Worker 和模型";
  document.querySelectorAll("[data-runtime]").forEach(button => { button.disabled = !fleet.runtime_configured; });
  el("agents").replaceChildren();
  for (const agent of fleet.agents) {
    const row = el("agents").insertRow();
    const button = node("button", agent.agent_id);
    button.onclick = () => perform(async () => {
      selected = agent.agent_id;
      await showTasks();
    });
    row.insertCell().append(button);
    for (const value of [agent.tenant_id, agent.state, date(agent.last_active_at)]) row.insertCell().textContent = value;
  }
  if (!fleet.agents.length) el("agents").insertRow().insertCell().textContent = "本页没有 Agent";
  next = fleet.agents.length === 100 ? fleet.agents.at(-1).agent_id : "";
  el("next").hidden = !next;
  el("schedules").replaceChildren();
  for (const item of schedules) {
    const card = node("div", "");
    card.className = "item";
    card.append(node("strong", item.content), node("p", "每 " + item.minutes + " 分钟 · 下次 " + date(item.next_run * 1000)));
    const remove = node("button", "删除");
    remove.onclick = () => {
      if (confirm("删除这条定时任务？")) perform(async () => {
        await api("schedules/" + segment(item.id), "DELETE");
        await refresh();
        report("定时任务已删除，已排队的任务可在 Agent 记录中查看。");
      });
    };
    card.append(remove);
    el("schedules").append(card);
  }
  if (!schedules.length) el("schedules").append(node("p", "暂无定时任务"));
  if (selected) await showTasks();
}

async function showTasks() {
  const id = selected;
  const tasks = await api("agents/" + segment(id) + "/tasks");
  if (id !== selected) return;
  el("detail").hidden = false;
  el("detail-title").textContent = id + " · 最近任务";
  el("tasks").replaceChildren();
  for (const task of tasks) {
    let prompt = "";
    try { prompt = JSON.parse(task.payload).content || ""; } catch {}
    const item = node("details", "");
    item.className = "item";
    item.append(node("summary", task.state + " · " + (prompt.slice(0, 100) || task.task_id)));
    item.append(node("p", task.task_id + " · " + date(task.updated_at)));
    item.append(node("pre", task.result || task.error_code || prompt || "暂无结果"));
    el("tasks").append(item);
  }
  if (!tasks.length) el("tasks").append(node("p", "暂无任务"));
}

async function perform(action) {
  try { await action(); } catch (error) { report(error.message, true); }
}

function form(id, action) {
  el(id).onsubmit = event => {
    event.preventDefault();
    const target = event.currentTarget;
    const button = target.querySelector("button");
    const values = Object.fromEntries(new FormData(target));
    button.disabled = true;
    perform(async () => {
      try { await action(values); } finally { button.disabled = false; }
    });
  };
}

form("auth", async () => {
  token = el("token").value.trim();
  el("token").value = "";
  await refresh();
  report("已连接");
});
form("create", async values => {
  const agent = await api("agents", "POST", values);
  selected = agent.agent_id;
  cursor = "";
  await refresh();
  report("已创建 " + agent.agent_id);
});
form("broadcast", async values => {
  if (!confirm("将此消息投递给所有 Agent？")) return;
  values.request_id = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
  const result = await api("messages/broadcast", "POST", values);
  el("broadcast").reset();
  report("已入队 " + result.queued + " 条任务。");
});
form("schedule", async values => {
  values.minutes = Number(values.minutes);
  values.first_run = new Date(values.first_run).toISOString();
  await api("schedules", "POST", values);
  el("schedule").reset();
  await refresh();
  report("定时任务已创建。");
});
form("message", async values => {
  const task = await api("agents/" + segment(selected) + "/messages", "POST", values);
  el("message").reset();
  await showTasks();
  report("已入队 " + task.task_id);
});
el("refresh").onclick = () => perform(async () => { cursor = ""; await refresh(); report("已刷新"); });
el("next").onclick = () => perform(async () => { cursor = next; await refresh(); });
perform(refresh);
