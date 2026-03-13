const { createApp, nextTick } = Vue;

function hideBootSplash() {
  const splash = document.getElementById("boot-splash");
  if (!splash || splash.classList.contains("hidden")) {
    return;
  }
  splash.classList.add("hidden");
  window.setTimeout(() => {
    splash.remove();
  }, 260);
}

function defaultBatchForm() {
  return {
    name: "",
    seedKeywords: "",
    publishedWithinDays: 90,
    keywordCap: 15,
    hidePreviouslySeen: true,
  };
}

createApp({
  data() {
    return {
      settings: {
        youtubeApiKey: "",
        proxyUrl: "",
        providerOrder: ["chatgpt", "gemini", "copilot", "perplexity"],
        defaultMarket: "en",
      },
      batches: [],
      batchForm: defaultBatchForm(),
      activeBatch: null,
      status: null,
      results: [],
      logs: [],
      suggestions: [],
      sort: "score",
      displayLimit: 48,
      chart: null,
      eventsSource: null,
      refreshTimer: null,
      loading: true,
      settingsModalOpen: false,
      savingSettings: false,
      runningBatch: false,
      restartingBatchId: null,
      stoppingBatchId: null,
      showPreviouslySeen: false,
      summaryCards: [
        { label: "Verified Shorts", value: "0" },
        { label: "Quota Units", value: "0" },
        { label: "Visible Videos", value: "0" },
      ],
    };
  },
  computed: {
    visibleResults() {
      return this.results.filter((item) => this.showPreviouslySeen || !item.hiddenByDefault);
    },
    featuredResults() {
      const limit = Math.max(1, Number(this.displayLimit) || 48);
      return this.visibleResults.slice(0, limit);
    },
    batchInProgress() {
      const status = String(this.status?.batch?.status || this.activeBatch?.status || "").toLowerCase();
      return this.runningBatch || this.stoppingBatchId !== null || status === "queued" || status === "pending" || status === "running";
    },
    hasBatchSeedKeywords() {
      return this.batchForm.seedKeywords.split(",").map((item) => item.trim()).filter(Boolean).length > 0;
    },
    latestLog() {
      return this.logs.length ? this.logs[this.logs.length - 1] : null;
    },
    batchProgress() {
      return this.inferBatchProgress(this.status?.batch || this.activeBatch, this.logs, this.results);
    },
  },
  watch: {
    results() {
      this.renderChartSoon();
    },
    showPreviouslySeen() {
      this.renderChartSoon();
    },
  },
  mounted() {
    this.init();
  },
  beforeUnmount() {
    if (this.eventsSource) {
      this.eventsSource.close();
    }
    if (this.refreshTimer) {
      clearTimeout(this.refreshTimer);
    }
    if (this.chart) {
      this.chart.dispose();
    }
  },
  methods: {
    normalizeSettings(payload) {
      const providerOrder = Array.isArray(payload?.providerOrder) ? payload.providerOrder.filter(Boolean) : [];
      while (providerOrder.length < 4) {
        providerOrder.push(["chatgpt", "gemini", "copilot", "perplexity"][providerOrder.length]);
      }
      return {
        youtubeApiKey: payload?.youtubeApiKey || "",
        proxyUrl: payload?.proxyUrl || "",
        providerOrder,
        defaultMarket: payload?.defaultMarket || "en",
      };
    },
    openSettingsModal() {
      this.settingsModalOpen = true;
    },
    closeSettingsModal() {
      this.settingsModalOpen = false;
    },
    async init() {
      try {
        await Promise.all([this.loadSettings(), this.loadBatches()]);
        if (this.batches.length > 0) {
          await this.selectBatch(this.batches[0], true);
        }
      } finally {
        this.loading = false;
        await nextTick();
        this.renderChart();
        hideBootSplash();
      }
    },
    async loadSettings() {
      const response = await fetch("/api/settings");
      this.settings = this.normalizeSettings(await response.json());
    },
    async loadBatches() {
      const response = await fetch("/api/batches?limit=20");
      const payload = await response.json();
      this.batches = Array.isArray(payload) ? payload : [];
    },
    syncBatchList(batch, totalResults = null) {
      if (!batch) {
        return;
      }
      const next = { ...batch };
      if (Number.isFinite(Number(totalResults))) {
        next.totalResults = Number(totalResults);
      }
      const index = this.batches.findIndex((item) => item.id === next.id);
      if (index >= 0) {
        this.batches.splice(index, 1, { ...this.batches[index], ...next });
      }
    },
    buildPayloadFromForm() {
      return {
        name: this.batchForm.name,
        seedKeywords: this.batchForm.seedKeywords.split(",").map((item) => item.trim()).filter(Boolean),
        publishedWithinDays: Number(this.batchForm.publishedWithinDays),
        keywordCap: Number(this.batchForm.keywordCap),
        hidePreviouslySeen: this.batchForm.hidePreviouslySeen,
      };
    },
    canResumeBatch(batch) {
      const status = String(batch?.status || "").toLowerCase();
      return !!batch && (status === "failed" || status === "stopped");
    },
    isBatchRunning(batch) {
      const status = String(batch?.status || "").toLowerCase();
      return status === "queued" || status === "pending" || status === "running";
    },
    isCompletedBatch(batch) {
      return String(batch?.status || "").toLowerCase() === "completed";
    },
    parseLogPayload(payload) {
      if (!payload) {
        return null;
      }
      try {
        return JSON.parse(payload);
      } catch (error) {
        return null;
      }
    },
    inferBatchProgress(batch, logs, results) {
      const steps = [
        { key: "queued", label: "Queued" },
        { key: "search", label: "Search" },
        { key: "expand", label: "AI Expand" },
        { key: "verify", label: "Verify" },
        { key: "done", label: "Done" },
      ];
      const status = String(batch?.status || "").toLowerCase();
      const latest = Array.isArray(logs) && logs.length ? logs[logs.length - 1] : null;
      const payload = this.parseLogPayload(latest?.payload);
      let percent = 0;
      let currentStep = 0;
      let active = false;
      let tone = "idle";
      let caption = "Waiting for a batch.";

      if (status === "queued" || status === "pending") {
        percent = 6;
        currentStep = 0;
        active = true;
        tone = "live";
        caption = "Queued and waiting to start.";
      } else if (status === "completed") {
        percent = 100;
        currentStep = 4;
        tone = "complete";
        caption = "Batch completed.";
      } else if (status === "stopped") {
        percent = 0;
        currentStep = 3;
        tone = "stopped";
        caption = "Stopped by user.";
      } else if (status === "failed") {
        percent = 0;
        currentStep = 3;
        tone = "error";
        caption = batch?.errorSummary ? String(batch.errorSummary) : "Batch failed.";
      } else if (status === "running") {
        percent = 12;
        currentStep = 1;
        active = true;
        tone = "live";
        caption = "Starting pipeline.";

        if (latest) {
          const stage = String(latest.stage || "").toLowerCase();
          const message = String(latest.message || "").toLowerCase();

          if (stage === "batch") {
            percent = 10;
            currentStep = 0;
            caption = "Batch is starting.";
          }
          if (stage === "youtube") {
            percent = 24;
            currentStep = 1;
            caption = "Searching YouTube candidates.";
            if (message.includes("video detail progress")) {
              const processed = Number(payload?.processed || 0);
              const total = Number(payload?.total || 0);
              percent = total > 0 ? Math.min(74, 56 + Math.round((processed / total) * 12)) : 60;
              currentStep = 3;
              caption = total > 0 ? `Loading video details ${processed}/${total}.` : "Loading video details.";
            }
          }
          if (stage === "keywords") {
            if (message.includes("rule keyword expansion complete")) {
              percent = 34;
              currentStep = 1;
              caption = "Search seeds expanded.";
            }
            if (message.includes("final keyword list ready")) {
              percent = 62;
              currentStep = 3;
              caption = "Keyword plan ready. Verifying videos next.";
            }
          }
          if (stage === "llm") {
            percent = message.includes("succeeded") || message.includes("reusing") ? 58 : 46;
            currentStep = 2;
            caption = message.includes("trying provider") ? "Free AI is expanding keywords." : "AI expansion in progress.";
          }
          if (stage === "verify") {
            const processed = Number(payload?.processed || 0);
            const total = Number(payload?.total || 0);
            percent = total > 0 ? Math.min(96, 70 + Math.round((processed / total) * 24)) : 82;
            currentStep = 3;
            caption = total > 0 ? `Verifying shorts ${processed}/${total}.` : "Verifying shorts.";
          }
          if (stage === "suggest") {
            percent = 97;
            currentStep = 4;
            caption = "Finalizing suggestions.";
          }
        }

        if ((Array.isArray(results) ? results.length : 0) > 0 && percent < 78) {
          percent = 78;
          currentStep = 3;
          caption = `Processing candidate videos. ${results.length} visible results so far.`;
        }
      }

      const completedStepCount = status === "completed" ? steps.length : Math.min(currentStep, steps.length - 1);
      return {
        active,
        percent,
        tone,
        caption,
        steps: steps.map((step, index) => {
          let state = "idle";
          if (status === "completed" || index < completedStepCount) {
            state = "done";
          } else if (index === currentStep) {
            state = active ? "current" : "idle";
          }
          return { ...step, state };
        }),
      };
    },
    async saveSettings() {
      this.savingSettings = true;
      try {
        const payload = {
          ...this.settings,
          proxyUrl: String(this.settings.proxyUrl || "").trim(),
        };
        const response = await fetch("/api/settings", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        if (!response.ok) {
          throw new Error((await response.json()).error);
        }
        this.settings = this.normalizeSettings(await response.json());
        this.settingsModalOpen = false;
      } catch (error) {
        alert(`Failed to save settings: ${error.message}`);
      } finally {
        this.savingSettings = false;
      }
    },
    async startBatch(payload, errorPrefix) {
      if (this.batchInProgress) {
        return;
      }
      if (!Array.isArray(payload.seedKeywords) || payload.seedKeywords.length === 0) {
        alert(`${errorPrefix}: seed keywords are required`);
        return;
      }
      this.runningBatch = true;
      this.logs = [];
      this.results = [];
      this.suggestions = [];
      try {
        const response = await fetch("/api/batches", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        if (!response.ok) {
          throw new Error((await response.json()).error);
        }
        this.activeBatch = await response.json();
        this.status = { batch: this.activeBatch, events: [] };
        await this.loadBatches();
        this.openEvents(this.activeBatch.id);
        await this.refreshBatch();
      } catch (error) {
        alert(`${errorPrefix}: ${error.message}`);
      } finally {
        this.runningBatch = false;
      }
    },
    async runBatch() {
      await this.startBatch(this.buildPayloadFromForm(), "Failed to start new batch");
    },
    async resumeBatch(batch) {
      if (!this.canResumeBatch(batch) || this.batchInProgress) {
        return;
      }
      this.restartingBatchId = batch.id;
      try {
        const response = await fetch(`/api/batches/${batch.id}/resume`, { method: "POST" });
        if (!response.ok) {
          throw new Error((await response.json()).error);
        }
        const resumedBatch = await response.json();
        this.activeBatch = resumedBatch;
        this.status = { batch: resumedBatch, events: this.logs };
        this.syncBatchList(resumedBatch, resumedBatch.totalResults);
        this.openEvents(batch.id);
        await this.refreshBatch();
        await this.loadBatches();
      } catch (error) {
        alert(`Failed to resume batch: ${error.message}`);
      } finally {
        this.restartingBatchId = null;
      }
    },
    canStopBatch(batch) {
      const status = String(batch?.status || "").toLowerCase();
      return !!batch && (status === "queued" || status === "pending" || status === "running");
    },
    async stopBatch(batch) {
      if (!this.canStopBatch(batch)) {
        return;
      }
      if (!window.confirm(`Stop batch "${batch.name}"?`)) {
        return;
      }
      this.stoppingBatchId = batch.id;
      try {
        const response = await fetch(`/api/batches/${batch.id}/stop`, { method: "POST" });
        if (!response.ok) {
          throw new Error((await response.json()).error);
        }
        if (this.activeBatch && this.activeBatch.id === batch.id) {
          this.scheduleRefreshBatch(250);
        } else {
          await this.loadBatches();
          setTimeout(async () => {
            try {
              await this.loadBatches();
              const current = this.batches.find((item) => item.id === batch.id);
              if (!this.canStopBatch(current)) {
                this.stoppingBatchId = null;
              }
            } catch (error) {
              console.error("load batches after stop failed", error);
            }
          }, 700);
        }
      } catch (error) {
        this.stoppingBatchId = null;
        alert(`Failed to stop batch: ${error.message}`);
      }
    },
    async selectBatch(batch, openStream) {
      this.activeBatch = batch;
      if (openStream) {
        this.openEvents(batch.id);
      }
      await this.refreshBatch();
    },
    openEvents(batchId) {
      if (this.eventsSource) {
        this.eventsSource.close();
      }
      this.eventsSource = new EventSource(`/api/batches/${batchId}/events`);
      this.eventsSource.addEventListener("log", async (event) => {
        const payload = JSON.parse(event.data);
        if (!this.activeBatch || payload.batchId !== this.activeBatch.id) {
          return;
        }
        this.logs.push(payload);
        if (this.logs.length > 200) {
          this.logs = this.logs.slice(-200);
        }
        if (payload.stage === "batch" && (payload.message === "batch completed" || payload.message === "batch stopped")) {
          await this.loadBatches();
          await this.refreshBatch();
          return;
        }
        this.scheduleRefreshBatch(payload.stage === "youtube" || payload.stage === "verify" ? 300 : 700);
      });
    },
    scheduleRefreshBatch(delay = 500) {
      if (!this.activeBatch) {
        return;
      }
      if (this.refreshTimer) {
        clearTimeout(this.refreshTimer);
      }
      this.refreshTimer = setTimeout(async () => {
        this.refreshTimer = null;
        try {
          await this.refreshBatch();
        } catch (error) {
          console.error("refresh batch failed", error);
        }
      }, delay);
    },
    async refreshBatch() {
      if (!this.activeBatch) {
        return;
      }
      const [statusRes, resultsRes] = await Promise.all([
        fetch(`/api/batches/${this.activeBatch.id}`),
        fetch(`/api/batches/${this.activeBatch.id}/results?sort=${this.sort}`),
      ]);
      this.status = await statusRes.json();
      this.activeBatch = this.status.batch;
      const resultsPayload = await resultsRes.json();
      this.results = Array.isArray(resultsPayload) ? resultsPayload : [];
      this.syncBatchList(this.status.batch, this.results.length);
      this.suggestions = Array.isArray(this.status?.batch?.suggestedKeywords) ? this.status.batch.suggestedKeywords : [];
      this.logs = Array.isArray(this.status?.events) ? this.status.events : this.logs;
      const batchStatus = String(this.status?.batch?.status || "").toLowerCase();
      if (batchStatus !== "queued" && batchStatus !== "pending" && batchStatus !== "running") {
        if (this.activeBatch && this.stoppingBatchId === this.activeBatch.id) {
          this.stoppingBatchId = null;
        }
      }
      const verifiedCount = this.results.filter((item) => item.verifiedShort === "true").length;
      this.summaryCards = [
        { label: "Verified Shorts", value: String(verifiedCount) },
        { label: "Quota Units", value: String(this.status.batch.searchQuotaUnits) },
        { label: "Visible Videos", value: String(this.results.length) },
      ];
      await nextTick();
      this.renderChart();
    },
    async changeSort(sort) {
      this.sort = sort;
      await this.refreshBatch();
    },
    canDeleteBatch(batch) {
      const status = String(batch?.status || "").toLowerCase();
      return !!batch && status !== "running" && status !== "queued";
    },
    async deleteBatch(batch) {
      if (!batch || !this.canDeleteBatch(batch)) {
        return;
      }
      if (!window.confirm(`Delete batch "${batch.name}"?`)) {
        return;
      }
      try {
        const response = await fetch(`/api/batches/${batch.id}`, { method: "DELETE" });
        if (!response.ok) {
          throw new Error((await response.json()).error);
        }
        this.batches = this.batches.filter((item) => item.id !== batch.id);
        if (this.activeBatch && this.activeBatch.id === batch.id) {
          if (this.eventsSource) {
            this.eventsSource.close();
            this.eventsSource = null;
          }
          this.activeBatch = this.batches[0] || null;
          this.status = null;
          this.results = [];
          this.logs = [];
          this.suggestions = [];
          this.stoppingBatchId = null;
          this.summaryCards = [
            { label: "Verified Shorts", value: "0" },
            { label: "Quota Units", value: "0" },
            { label: "Visible Videos", value: "0" },
          ];
          if (this.activeBatch) {
            await this.selectBatch(this.activeBatch, true);
          }
        }
      } catch (error) {
        alert(`Failed to delete batch: ${error.message}`);
      }
    },
    async minimizeWindow() {
      if (typeof window.appMinimize === "function") {
        await window.appMinimize();
      }
    },
    async closeWindow() {
      if (typeof window.appClose === "function") {
        await window.appClose();
      }
    },
    renderChartSoon() {
      nextTick(() => this.renderChart());
    },
    renderChart() {
      const target = document.getElementById("score-chart");
      if (!target) return;
      if (!this.chart) {
        this.chart = echarts.init(target);
        window.addEventListener("resize", () => this.chart && this.chart.resize());
      }
      const palette = ["#38bdf8", "#22c55e", "#facc15", "#f97316", "#fb7185"];
      const chartItems = this.visibleResults.slice(0, 10).map((item) => ({
        name: item.title,
        value: [item.viewsPerDay, item.breakoutScore, item.views],
      }));
      const sortedScores = chartItems
        .map((item) => Number(item.value[1]) || 0)
        .sort((left, right) => left - right);
      const colorSeries = palette.map((color, index) => ({
        name: `Band ${index + 1}`,
        color,
        data: [],
      }));
      chartItems.forEach((item) => {
        const score = Number(item.value[1]) || 0;
        const rank = sortedScores.findIndex((value) => value >= score);
        const normalized = sortedScores.length <= 1 ? 1 : rank / (sortedScores.length - 1);
        const bucketIndex = Math.min(colorSeries.length - 1, Math.max(0, Math.round(normalized * (colorSeries.length - 1))));
        colorSeries[bucketIndex].data.push(item);
      });
      this.chart.setOption({
        backgroundColor: "transparent",
        legend: {
          top: 0,
          right: 0,
          itemWidth: 10,
          itemHeight: 10,
          selectedMode: false,
          textStyle: { color: "rgba(255,255,255,0.62)", fontSize: 11 },
        },
        tooltip: {
          trigger: "item",
          formatter: (params) => `${this.escapeHTML(params.name)}<br>Views/Day: ${params.value[0].toFixed(0)}<br>Score: ${params.value[1].toFixed(2)}<br>Total Views: ${this.formatCount(params.value[2])}`,
        },
        grid: {
          left: 54,
          right: 34,
          top: 36,
          bottom: 34,
        },
        xAxis: {
          type: "value",
          name: "Views/Day",
          axisLabel: { color: "rgba(255,255,255,0.55)" },
          nameTextStyle: { color: "rgba(255,255,255,0.65)" },
        },
        yAxis: {
          type: "value",
          name: "Breakout Score",
          axisLabel: { color: "rgba(255,255,255,0.55)" },
          nameTextStyle: { color: "rgba(255,255,255,0.65)" },
        },
        series: colorSeries.filter((bucket) => bucket.data.length > 0).map((bucket) => ({
          name: bucket.name,
          type: "scatter",
          symbolSize(value) {
            return Math.max(4, Math.min(8, Math.log10(value[2] + 10) * 1.6));
          },
          emphasis: { scale: 1.12 },
          itemStyle: {
            color: bucket.color,
            opacity: 0.92,
            shadowBlur: 8,
            shadowColor: `${bucket.color}66`,
          },
          data: bucket.data,
        })),
      }, true);
    },
    fallbackThumbnail(item) {
      return item.thumbnail || `https://i.ytimg.com/vi/${item.videoId}/hqdefault.jpg`;
    },
    formatCount(value) {
      return new Intl.NumberFormat().format(Number(value) || 0);
    },
    formatCompact(value) {
      return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(Number(value) || 0);
    },
    formatDate(value) {
      return value ? new Date(value).toLocaleDateString() : "";
    },
    formatStatus(status) {
      return String(status || "idle").replaceAll("_", " ");
    },
    formatVerifiedStatus(status) {
      switch (String(status || "").toLowerCase()) {
        case "true":
          return "verified short";
        case "unknown":
          return "unverified";
        case "false":
          return "not short";
        default:
          return "unknown";
      }
    },
    isActiveBatch(batch) {
      return this.activeBatch && batch && this.activeBatch.id === batch.id;
    },
    escapeHTML(value) {
      return String(value || "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;")
        .replaceAll("'", "&#39;");
    },
  },
  template: `
    <div class="app-shell">
      <div class="window-controls">
        <button class="window-control" type="button" title="Minimize" @click="minimizeWindow">_</button>
        <button class="window-control close" type="button" title="Close" @click="closeWindow">X</button>
      </div>

      <section class="panel">
        <div class="panel-header">
          <div class="panel-header-row">
            <div class="brand mb-0">
              <div>
                <div class="hero-chip">Windows Local Radar</div>
                <h1 class="mt-2">AI YouTube Shorts Radar</h1>
              </div>
            </div>
            <button class="settings-trigger" type="button" title="Settings" aria-label="Open settings" @click="openSettingsModal">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M12 3.75a1.75 1.75 0 0 1 1.71 1.38l.16.77a1.75 1.75 0 0 0 2.46 1.2l.69-.35a1.75 1.75 0 0 1 2.15.38l.7.7a1.75 1.75 0 0 1 .38 2.15l-.35.69a1.75 1.75 0 0 0 1.2 2.46l.77.16A1.75 1.75 0 0 1 20.25 12a1.75 1.75 0 0 1-1.38 1.71l-.77.16a1.75 1.75 0 0 0-1.2 2.46l.35.69a1.75 1.75 0 0 1-.38 2.15l-.7.7a1.75 1.75 0 0 1-2.15.38l-.69-.35a1.75 1.75 0 0 0-2.46 1.2l-.16.77A1.75 1.75 0 0 1 12 20.25a1.75 1.75 0 0 1-1.71-1.38l-.16-.77a1.75 1.75 0 0 0-2.46-1.2l-.69.35a1.75 1.75 0 0 1-2.15-.38l-.7-.7a1.75 1.75 0 0 1-.38-2.15l.35-.69a1.75 1.75 0 0 0-1.2-2.46l-.77-.16A1.75 1.75 0 0 1 3.75 12a1.75 1.75 0 0 1 1.38-1.71l.77-.16a1.75 1.75 0 0 0 1.2-2.46l-.35-.69a1.75 1.75 0 0 1 .38-2.15l.7-.7a1.75 1.75 0 0 1 2.15-.38l.69.35a1.75 1.75 0 0 0 2.46-1.2l.16-.77A1.75 1.75 0 0 1 12 3.75Z"></path>
                <circle cx="12" cy="12" r="3.25"></circle>
              </svg>
            </button>
          </div>
          <p class="subtle">Open settings once, then create and control batches from here.</p>
        </div>

        <div class="panel-body">
          <div class="sidebar-stack">
            <section>
              <div class="section-title">Create New Batch</div>
              <div class="mb-3">
                <label class="form-label">Batch Name</label>
                <input v-model="batchForm.name" class="form-control" placeholder="March experiment" />
              </div>
              <div class="mb-3">
                <label class="form-label">Seed Keywords</label>
                <textarea v-model="batchForm.seedKeywords" class="form-control mono" placeholder="cycling shorts, bmx tricks"></textarea>
                <div class="form-hint">Comma-separated English seeds. Example: cycling shorts, bmx tricks.</div>
              </div>
              <div class="row g-2">
                <div class="col-6">
                  <label class="form-label">Lookback Days</label>
                  <input v-model="batchForm.publishedWithinDays" type="number" min="7" max="365" class="form-control mono" />
                </div>
                <div class="col-6">
                  <label class="form-label">Keyword Cap</label>
                  <input v-model="batchForm.keywordCap" type="number" min="5" max="30" class="form-control mono" />
                </div>
              </div>
              <label class="form-check mt-3">
                <input v-model="batchForm.hidePreviouslySeen" class="form-check-input" type="checkbox">
                <span class="form-check-label">Hide previously seen videos by default</span>
              </label>
              <div class="form-hint mt-3">New batches appear in Recent Batches on the right.</div>
            </section>

            <div class="action-stack">
              <div class="control-row">
                <button class="btn primary-cta btn-orange" type="button" :disabled="batchInProgress || !hasBatchSeedKeywords" @click="runBatch">
                  {{ runningBatch ? 'Creating and Running...' : 'Create and Run' }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-header">
          <div class="results-header">
            <div>
              <div class="hero-chip">Current Batch</div>
              <h2 class="mt-2 mb-1">{{ activeBatch ? activeBatch.name : 'No batch selected' }}</h2>
              <div class="subtle status-line">
                <span class="status-pill" :class="batchProgress.tone">
                  <span v-if="batchProgress.active" class="status-pulse" aria-hidden="true"></span>
                  {{ status?.batch?.status || 'idle' }}
                </span>
                <span v-if="status?.batch?.createdAt">| {{ formatDate(status.batch.createdAt) }}</span>
              </div>
              <div v-if="latestLog" class="subtle mt-1">{{ latestLog.stage }}: {{ latestLog.message }}</div>
            </div>
            <div class="sort-actions">
              <button class="btn" :class="sort === 'score' ? 'btn-light' : 'btn-outline-light'" @click="changeSort('score')">Sort: Score</button>
              <button class="btn" :class="sort === 'views' ? 'btn-light' : 'btn-outline-light'" @click="changeSort('views')">Sort: Views</button>
              <button class="btn" :class="sort === 'views_per_day' ? 'btn-light' : 'btn-outline-light'" @click="changeSort('views_per_day')">Sort: Views/Day</button>
              <label class="limit-control">
                <span>Top</span>
                <input v-model.number="displayLimit" type="number" min="1" max="500" class="form-control mono limit-input">
              </label>
            </div>
          </div>
        </div>

        <div class="panel-body">
          <div v-if="activeBatch" class="progress-card">
            <div class="progress-head">
              <div>
                <div class="progress-label">Pipeline Progress</div>
                <div class="progress-caption">{{ batchProgress.caption }}</div>
              </div>
              <div class="progress-value">{{ batchProgress.percent }}%</div>
            </div>
            <div class="progress-track" :class="{ live: batchProgress.active }">
              <div class="progress-fill" :style="{ width: batchProgress.percent + '%' }"></div>
            </div>
            <div class="progress-steps">
              <span v-for="step in batchProgress.steps" :key="step.key" class="progress-step" :class="step.state">{{ step.label }}</span>
            </div>
          </div>

          <div class="stat-grid">
            <div class="stat-card" v-for="card in summaryCards" :key="card.label">
              <div class="stat-label">{{ card.label }}</div>
              <div class="stat-value">{{ card.value }}</div>
            </div>
          </div>

          <div class="chart-card">
            <div class="section-title">Breakout Map</div>
            <div id="score-chart"></div>
          </div>

          <div class="toggle-row">
            <div>
              <div class="section-title mb-1">Top Videos</div>
              <div class="subtle">Large thumbnails first. Showing {{ featuredResults.length }} / {{ visibleResults.length }}. Current order follows the selected sort.</div>
            </div>
            <label class="form-check">
              <input v-model="showPreviouslySeen" class="form-check-input" type="checkbox">
              <span class="form-check-label">Show seen</span>
            </label>
          </div>

          <div v-if="!featuredResults.length" class="empty-state">
            No videos to display yet. Start a batch from the left side or open a batch from the right-side project list.
          </div>
          <div v-else class="results-wall">
            <a v-for="(item, index) in featuredResults" :key="item.videoId + '-' + index" class="video-card" :href="item.shortsLink || item.watchLink" target="_blank" rel="noreferrer">
              <div class="video-thumb-wrap">
                <img class="video-thumb" :src="fallbackThumbnail(item)" :alt="item.title">
                <div class="video-rank">#{{ index + 1 }}</div>
                <div class="video-views">{{ formatCompact(item.views) }} views</div>
              </div>
              <div class="video-meta">
                <div class="video-title">{{ item.title }}</div>
                <div class="video-channel">
                  <span>{{ item.channel }}</span>
                  <span>{{ formatDate(item.publishedAt) }}</span>
                </div>
                <div class="video-stats">
                  <span>{{ formatCount(item.views) }}</span>
                  <span>{{ item.breakoutScore.toFixed(2) }} score</span>
                  <span>{{ formatVerifiedStatus(item.verifiedShort) }}</span>
                </div>
              </div>
            </a>
          </div>

        </div>
      </section>

      <section class="panel">
        <div class="panel-header">
          <div class="hero-chip">Progress Stream</div>
          <h2 class="mt-2 mb-1">Recent Batches</h2>
        </div>

        <div class="panel-body">
          <div class="batch-list mb-4">
            <button v-if="!batches.length" class="batch-item" type="button" disabled>
              <div class="batch-item-title">No batches yet</div>
              <div class="batch-item-meta">Run your first batch from the left panel.</div>
            </button>
            <div v-for="batch in batches" :key="batch.id" class="batch-entry" :class="{ 'can-delete': canDeleteBatch(batch) }">
              <div class="batch-leading-slot">
                <button v-if="canStopBatch(batch)" class="batch-leading-action" :class="{ running: isBatchRunning(batch) }" type="button" :disabled="stoppingBatchId === batch.id" title="Stop batch" aria-label="Stop batch" @click.stop="stopBatch(batch)">
                  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                    <rect x="6.5" y="6.5" width="11" height="11" rx="1.8"></rect>
                  </svg>
                </button>
                <button v-else-if="canResumeBatch(batch)" class="batch-leading-action resume" type="button" :disabled="batchInProgress || restartingBatchId === batch.id" title="Start batch" aria-label="Start batch" @click.stop="resumeBatch(batch)">
                  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                    <path d="M8 5.5v13l10-6.5z"></path>
                  </svg>
                </button>
                <button v-else-if="isCompletedBatch(batch)" class="batch-leading-action completed" type="button" disabled title="Completed batch" aria-label="Completed batch">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M6.5 12.5l3.5 3.5 7.5-8"></path>
                  </svg>
                </button>
              </div>
              <button class="batch-item" :class="{ active: isActiveBatch(batch) }" type="button" @click="selectBatch(batch, true)">
                <div class="batch-item-title">{{ batch.name }}</div>
                <div class="batch-item-meta">{{ formatStatus(batch.status) }} | {{ formatDate(batch.createdAt) }} | {{ batch.totalResults }} results</div>
              </button>
              <button v-if="canDeleteBatch(batch)" class="batch-delete" type="button" title="Delete batch" aria-label="Delete batch" @click.stop="deleteBatch(batch)">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                  <path d="M3 6h18"></path>
                  <path d="M8 6V4.75C8 3.78 8.78 3 9.75 3h4.5C15.22 3 16 3.78 16 4.75V6"></path>
                  <path d="M19 6l-1 13.25A2 2 0 0 1 16.01 21H7.99A2 2 0 0 1 6 19.25L5 6"></path>
                  <path d="M10 10.5v6"></path>
                  <path d="M14 10.5v6"></path>
                </svg>
              </button>
            </div>
          </div>

          <div class="section-title">Suggested Next Keywords</div>
          <div class="suggestions mb-4">
            <span v-if="!suggestions.length" class="badge bg-dark-lt text-secondary">No suggestions yet</span>
            <span v-for="term in suggestions" :key="term" class="badge bg-orange-lt text-orange">{{ term }}</span>
          </div>

          <div class="section-title">Batch Log</div>
          <div class="log-list">
            <div v-if="!logs.length" class="subtle">Logs appear here while the batch runs.</div>
            <div v-for="entry in logs" :key="entry.id + '-' + entry.sequence" class="log-item" :class="entry.level">
              <div class="d-flex justify-content-between gap-2">
                <div>
                  <div class="fw-bold text-capitalize">{{ entry.stage }}</div>
                  <div>{{ entry.message }}</div>
                </div>
                <div class="mono subtle text-end">{{ new Date(entry.createdAt).toLocaleTimeString() }}</div>
              </div>
              <div v-if="entry.payload" class="mono subtle mt-2">{{ entry.payload }}</div>
            </div>
          </div>
        </div>
      </section>

      <div v-if="settingsModalOpen" class="settings-modal-backdrop" @click.self="closeSettingsModal">
        <div class="panel settings-modal">
          <div class="settings-modal-header">
            <div>
              <div class="hero-chip">Settings</div>
              <h2 class="mt-2 mb-1">App Settings</h2>
              <div class="subtle">These settings apply to the next batch you start.</div>
            </div>
            <button class="settings-trigger" type="button" title="Close settings" aria-label="Close settings" @click="closeSettingsModal">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M6 6l12 12"></path>
                <path d="M18 6l-12 12"></path>
              </svg>
            </button>
          </div>

          <div class="mb-3">
            <label class="form-label">YouTube API Key</label>
            <input v-model="settings.youtubeApiKey" class="form-control mono" placeholder="AIza..." />
            <div class="form-hint">You can apply for a free YouTube Data API key in Google Cloud.</div>
          </div>

          <div class="mb-3">
            <label class="form-label">Proxy</label>
            <input v-model="settings.proxyUrl" class="form-control mono" placeholder="127.0.0.1:10601" />
            <div class="form-hint">You can enter <span class="mono">127.0.0.1:10601</span> directly. The app will try SOCKS5 first, then fall back to HTTP. Leave it blank to disable the proxy. Full values like <span class="mono">socks5://...</span> or <span class="mono">http://...</span> also work.</div>
          </div>

          <div class="mb-1">
            <label class="form-label">Language</label>
            <input v-model="settings.defaultMarket" class="form-control mono" maxlength="8" placeholder="en" />
            <div class="form-hint">Examples: <span class="mono">en</span>, <span class="mono">ja</span>, <span class="mono">ko</span>, <span class="mono">es</span>, <span class="mono">pt</span>.</div>
          </div>

          <div class="settings-modal-actions">
            <button class="btn btn-outline-light" type="button" @click="closeSettingsModal">Close</button>
            <button class="btn btn-warning" type="button" :disabled="savingSettings" @click="saveSettings">
              {{ savingSettings ? 'Saving...' : 'Save Settings' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  `,
}).mount("#app");
