import {
  Component,
  Input,
  ChangeDetectorRef,
  ElementRef,
  ViewChild,
  HostListener,
  inject,
} from "@angular/core";
import { FormsModule } from "@angular/forms";
import {
  api,
  Catalog,
  Dashboard,
  Service,
  Extra,
  Specialist,
  salonDate,
  timeLabel,
  toInstant,
  statuses,
} from "./api";

type Edit = {
  id: number;
  name: string;
  description: string;
  bio: string;
  active: boolean;
  price: number;
  minutes: number;
  p60: number | null;
  p90: number | null;
  p120: number | null;
};
interface BotSettings {
  welcome_message: string;
  contact_text: string;
  services_enabled: boolean;
  reminder_24h: boolean;
  reminder_2h: boolean;
}
interface BotInfo {
  settings: BotSettings;
  configured: boolean;
  username: string;
  stats: Record<string, number>;
}
@Component({
  selector: "app-admin",
  standalone: true,
  imports: [FormsModule],
  templateUrl: "./admin.html",
})
export class AdminComponent {
  @Input() catalog: Catalog = {
    services: [],
    extras: [],
    specialists: [],
    timezone: "Asia/Bishkek",
    currency: "KGS",
    telegram_bot: "",
  };
  @ViewChild("sidebar") sidebar?: ElementRef<HTMLElement>;
  @ViewChild("menuToggle") menuToggle?: ElementRef<HTMLButtonElement>;
  private cd = inject(ChangeDetectorRef);
  logged = false;
  checking = true;
  busy = false;
  error = "";
  notice = "";
  password = "";
  date = salonDate("Asia/Bishkek");
  tab = "overview";
  navOpen = false;
  data: Dashboard | null = null;
  bot: BotInfo | null = null;
  botDraft: BotSettings | null = null;
  botDirty = false;
  statuses = statuses;
  filter = "";
  search = "";
  shiftMaster = 0;
  shiftStart = "09:00";
  shiftEnd = "18:00";
  editor: Edit | null = null;
  editKind = "services";
  menu = [
    {
      id: "overview",
      label: "Дашборд",
      group: "Обзор",
      icon: "M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z",
      description: "Главное о работе студии в одном месте",
    },
    {
      id: "bookings",
      label: "Записи",
      group: "Работа студии",
      icon: "M8 3h8v4H8z M8 5H5v16h14V5h-3 M8 12h8 M8 16h5",
      description: "Визиты, контакты гостей и статусы сеансов",
    },
    {
      id: "schedule",
      label: "Расписание",
      group: "",
      icon: "M3 6h18v15H3z M7 3v6 M17 3v6 M3 11h18 M8 15h2 M14 15h2",
      description: "Рабочие интервалы мастеров, перерывы и выходные",
    },
    {
      id: "services",
      label: "Услуги",
      group: "",
      icon: "M12 3v18 M3 12h18 M6 6l12 12 M6 18L18 6",
      description: "Массажи, длительность сеансов и стоимость",
    },
    {
      id: "extras",
      label: "Дополнительные опции",
      group: "",
      icon: "M12 4v16 M4 12h16",
      description: "Дополнения к сеансу: стоимость и дополнительное время",
    },
    {
      id: "specialists",
      label: "Специалисты",
      group: "",
      icon: "M16 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0 M4 21v-3a8 8 0 0 1 16 0v3",
      description: "Команда студии и доступность для новых записей",
    },
    {
      id: "bot",
      label: "Telegram-бот",
      group: "Коммуникации",
      icon: "M3 10l18-7-5 18-5-7-8-4z M11 14l10-11",
      description: "Подключение, сообщения и напоминания гостям",
    },
  ];
  private hashChange = () => {
    const id = location.hash.slice(1);
    this.tab = this.menu.some((m) => m.id === id) ? id : "overview";
    this.navOpen = false;
    this.editor = null;
    this.error = "";
    this.notice = "";
    this.cd.markForCheck();
  };
  ngOnInit() {
    this.date = salonDate(this.catalog.timezone);
    this.hashChange();
    window.addEventListener("hashchange", this.hashChange);
    void this.probe();
  }
  ngOnDestroy() {
    window.removeEventListener("hashchange", this.hashChange);
  }
  @HostListener("document:keydown.escape") closeMenu() {
    if (this.navOpen) {
      this.navOpen = false;
      this.menuToggle?.nativeElement.focus();
    }
  }
  toggleMenu() {
    this.navOpen = !this.navOpen;
    if (this.navOpen)
      queueMicrotask(() =>
        this.sidebar?.nativeElement
          .querySelector<HTMLAnchorElement>(".sidebar-nav a")
          ?.focus(),
      );
  }
  select(id: string) {
    this.navOpen = false;
    this.tab = id;
    this.editor = null;
    this.error = "";
    this.notice = "";
    if (location.hash !== "#" + id) location.hash = id;
  }
  get page() {
    return this.menu.find((m) => m.id === this.tab)!;
  }
  get dateSection() {
    return ["overview", "bookings", "schedule"].includes(this.tab);
  }
  get activeServices() {
    return this.data?.catalog.services.filter((s) => s.active).length || 0;
  }
  get activeMasters() {
    return this.data?.catalog.specialists.filter((s) => s.active).length || 0;
  }
  get weekTotal() {
    return this.data?.weekly.reduce((n, d) => n + d.bookings, 0) || 0;
  }
  get weekMax() {
    return Math.max(1, ...(this.data?.weekly.map((d) => d.bookings) || []));
  }
  get upcoming() {
    return (
      this.data?.bookings.filter((b) => b.status === "confirmed").slice(0, 5) ||
      []
    );
  }
  get filteredBookings() {
    const q = this.search.trim().toLowerCase();
    return (
      this.data?.bookings.filter(
        (b) =>
          (!this.filter || b.status === this.filter) &&
          (!q ||
            [b.name, b.phone, b.service, b.specialist]
              .join(" ")
              .toLowerCase()
              .includes(q)),
      ) || []
    );
  }
  get scheduledHours() {
    return (
      this.data?.shifts.reduce(
        (n, s) =>
          n + (Date.parse(s.ends_at) - Date.parse(s.starts_at)) / 3600000,
        0,
      ) || 0
    ).toLocaleString("ru", { maximumFractionDigits: 1 });
  }
  async probe() {
    try {
      await this.reload();
      this.logged = true;
      await this.loadBot();
    } catch (e) {
      if (this.logged) this.error = this.message(e);
    } finally {
      this.checking = false;
      this.cd.markForCheck();
    }
  }
  message(e: unknown) {
    return e instanceof Error ? e.message : "Не удалось связаться с сервером";
  }
  time(v: string) {
    return timeLabel(v, this.catalog.timezone);
  }
  dayLabel(v: string) {
    return new Intl.DateTimeFormat("ru", {
      weekday: "short",
      day: "numeric",
      timeZone: "UTC",
    }).format(new Date(v + "T12:00:00Z"));
  }
  money(n: number) {
    return (
      new Intl.NumberFormat("ru").format(n) +
      " " +
      (this.catalog.currency === "KGS" ? "сом" : this.catalog.currency)
    );
  }
  master(id: number) {
    return (
      this.data?.catalog.specialists.find((s) => s.id === id)?.name || "Мастер"
    );
  }
  async run(fn: () => Promise<void>) {
    if (this.busy) return;
    this.busy = true;
    this.error = "";
    this.notice = "";
    try {
      await fn();
    } catch (e) {
      this.error = this.message(e);
      if (this.error.includes("Сессия") || this.error.includes("Войдите"))
        this.logged = false;
    } finally {
      this.busy = false;
      this.cd.markForCheck();
    }
  }
  async reload() {
    const config = await api<Catalog>("/admin/catalog");
    if (this.catalog.timezone !== config.timezone)
      this.date = salonDate(config.timezone);
    this.catalog = config;
    this.data = await api<Dashboard>("/admin/dashboard?date=" + this.date);
    if (!this.shiftMaster)
      this.shiftMaster = this.data.catalog.specialists[0]?.id || 0;
  }
  async loadBot() {
    this.bot = await api<BotInfo>("/admin/bot");
    if (!this.botDirty) this.botDraft = { ...this.bot.settings };
  }
  refresh() {
    void this.run(() => this.reload());
  }
  login() {
    void this.run(async () => {
      await api("/admin/login", "POST", { password: this.password });
      this.password = "";
      this.logged = true;
      await Promise.all([this.reload(), this.loadBot()]);
    });
  }
  logout() {
    void this.run(async () => {
      await api("/admin/logout", "POST", {});
      this.logged = false;
      this.data = null;
      this.bot = null;
      this.botDraft = null;
      this.botDirty = false;
      this.navOpen = false;
    });
  }
  addShift() {
    void this.run(async () => {
      await api("/admin/shifts", "POST", {
        id: 0,
        specialist_id: this.shiftMaster,
        starts_at: toInstant(
          this.date + "T" + this.shiftStart,
          this.catalog.timezone,
        ),
        ends_at: toInstant(
          this.date + "T" + this.shiftEnd,
          this.catalog.timezone,
        ),
      });
      await this.reload();
      this.notice = "Рабочий интервал добавлен";
    });
  }
  removeShift(id: number) {
    void this.run(async () => {
      await api("/admin/shifts/" + id, "DELETE");
      await this.reload();
      this.notice = "Рабочий интервал удалён";
    });
  }
  status(id: number, status: string) {
    if (
      status === "cancelled" &&
      !window.confirm("Отменить запись и уведомить клиента?")
    )
      return;
    void this.run(async () => {
      await api("/admin/bookings/" + id + "/status", "POST", { status });
      await this.reload();
      this.notice = "Статус записи обновлён";
    });
  }
  edit(kind: string, item?: Service | Extra | Specialist) {
    this.editKind = kind;
    const s = item as Service | undefined;
    const x = item as Extra | undefined;
    this.editor = {
      id: item?.id || 0,
      name: item?.name || "",
      description: s?.description || "",
      bio: (item as Specialist | undefined)?.bio || "",
      active: item?.active ?? true,
      price: x?.price || 0,
      minutes: x?.minutes || 0,
      p60: s?.prices?.["60"] ?? null,
      p90: s?.prices?.["90"] ?? null,
      p120: s?.prices?.["120"] ?? null,
    };
  }
  save() {
    void this.run(async () => {
      const x = this.editor;
      if (!x) return;
      const prices: Record<string, number> = {};
      for (const [d, p] of [
        ["60", x.p60],
        ["90", x.p90],
        ["120", x.p120],
      ] as const) {
        if (p !== null) prices[d] = p;
      }
      await api("/admin/" + this.editKind, "POST", {
        id: x.id,
        name: x.name,
        active: x.active,
        ...(this.editKind === "services"
          ? { description: x.description, prices }
          : this.editKind === "extras"
            ? { price: x.price, minutes: x.minutes }
            : { bio: x.bio }),
      });
      await this.reload();
      this.editor = null;
      this.notice = "Изменения сохранены. Каталог бота обновлён.";
    });
  }
  prices(s: Service) {
    return Object.entries(s.prices)
      .map(([d, p]) => d + " мин · " + this.money(p))
      .join(" / ");
  }
  saveBot() {
    void this.run(async () => {
      if (!this.botDraft) return;
      await api("/admin/bot", "PUT", this.botDraft);
      this.botDirty = false;
      await this.loadBot();
      this.notice = "Настройки бота сохранены и применяются без перезапуска";
    });
  }
  checkBot() {
    void this.run(async () => {
      const result = await api<{ username: string }>(
        "/admin/bot/check",
        "POST",
        {},
      );
      await this.loadBot();
      this.notice = "Telegram подтвердил подключение к @" + result.username;
    });
  }
  retryJobs() {
    void this.run(async () => {
      const result = await api<{ retried: number }>(
        "/admin/bot/retry",
        "POST",
        {},
      );
      await this.loadBot();
      this.notice = "Повторно поставлено в очередь: " + result.retried;
    });
  }
  get botPreview() {
    if (!this.botDraft) return "";
    return (
      this.botDraft.welcome_message +
      (this.botDraft.services_enabled ? "\n\n/services — услуги и цены" : "") +
      "\n\nНажмите «Записаться» — выберите услугу, мастера и удобное время прямо здесь." +
      (this.botDraft.contact_text ? "\n\n" + this.botDraft.contact_text : "")
    );
  }
}
