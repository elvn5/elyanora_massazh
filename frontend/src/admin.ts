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
import { NgIcon, provideIcons } from "@ng-icons/core";
import {
  lucideCalendarDays,
  lucideMessageCircle,
  lucidePhone,
  lucideRotateCcw,
  lucideSearch,
  lucideUserRound,
  lucideX,
} from "@ng-icons/lucide";
import { HlmAlertImports } from "@spartan-ng/helm/alert";
import { HlmAlertDialogImports } from "@spartan-ng/helm/alert-dialog";
import { HlmBadgeImports } from "@spartan-ng/helm/badge";
import { HlmButtonImports } from "@spartan-ng/helm/button";
import { HlmCheckboxImports } from "@spartan-ng/helm/checkbox";
import { HlmInputImports } from "@spartan-ng/helm/input";
import { HlmInputGroupImports } from "@spartan-ng/helm/input-group";
import { HlmLabelImports } from "@spartan-ng/helm/label";
import { HlmSwitchImports } from "@spartan-ng/helm/switch";
import { HlmTableImports } from "@spartan-ng/helm/table";
import { HlmTextareaImports } from "@spartan-ng/helm/textarea";
import { AppSelect, SelectOption } from "./admin-select";
import {
  api,
  Booking,
  Catalog,
  Client,
  Dashboard,
  Page,
  Service,
  Extra,
  Specialist,
  dateLabel,
  salonDate,
  timeLabel,
  toInstant,
  statuses,
} from "./api";

type Period = "day" | "today" | "week" | "month" | "past" | "all" | "custom";
/** Status colors shared by badges and select dots. */
export const statusTone: Record<string, string> = {
  confirmed: "var(--color-brand)",
  completed: "var(--color-success)",
  cancelled: "var(--color-neutral-ink)",
  no_show: "var(--color-danger)",
};

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
  imports: [
    FormsModule,
    NgIcon,
    AppSelect,
    HlmAlertImports,
    HlmAlertDialogImports,
    HlmBadgeImports,
    HlmButtonImports,
    HlmCheckboxImports,
    HlmInputImports,
    HlmInputGroupImports,
    HlmLabelImports,
    HlmSwitchImports,
    HlmTableImports,
    HlmTextareaImports,
  ],
  providers: [
    provideIcons({
      lucideCalendarDays,
      lucideMessageCircle,
      lucidePhone,
      lucideRotateCcw,
      lucideSearch,
      lucideUserRound,
      lucideX,
    }),
  ],
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
  statusTone = statusTone;
  // Bookings search: any period, text, status, specialist, service, client.
  bq = {
    q: "",
    period: "week" as Period,
    from: "",
    to: "",
    status: "" as string,
    specialist: 0,
    service: 0,
    sort: "asc",
    phone: "",
    phoneLabel: "",
  };
  bookingPage: Page<Booking> = { items: [], total: 0, revenue: 0 };
  bookingsLoading = false;
  // Clients: grouped by phone with segments.
  cq = { q: "", segment: "all", sort: "recent" };
  clientPage: Page<Client> = { items: [], total: 0 };
  clientsLoading = false;
  private searchTimer?: ReturnType<typeof setTimeout>;
  private bookingSeq = 0;
  private clientSeq = 0;
  // Confirmation dialog replacing window.confirm.
  confirmCancel: Booking | null = null;
  readonly periodOptions: SelectOption[] = [
    { value: "day", label: "Выбранный день", hint: "Дата из шапки страницы" },
    { value: "today", label: "Сегодня" },
    { value: "week", label: "Ближайшие 7 дней" },
    { value: "month", label: "Ближайшие 30 дней" },
    { value: "past", label: "Прошедшие 30 дней" },
    { value: "all", label: "За всё время" },
    { value: "custom", label: "Свой период", hint: "Укажите даты начала и конца" },
  ];
  readonly statusOptions: SelectOption[] = [
    { value: "", label: "Все статусы" },
    { value: "confirmed", label: "Подтверждены", dot: statusTone["confirmed"] },
    { value: "completed", label: "Завершены", dot: statusTone["completed"] },
    { value: "cancelled", label: "Отменены", dot: statusTone["cancelled"] },
    { value: "no_show", label: "Неявки", dot: statusTone["no_show"] },
  ];
  readonly sortOptions: SelectOption[] = [
    { value: "asc", label: "Сначала ранние" },
    { value: "desc", label: "Сначала поздние" },
  ];
  readonly segmentOptions: SelectOption[] = [
    { value: "all", label: "Все клиенты" },
    { value: "upcoming", label: "С предстоящей записью", hint: "Есть подтверждённый визит впереди" },
    { value: "regular", label: "Постоянные", hint: "Три и больше завершённых визита" },
    { value: "telegram", label: "В Telegram", hint: "Получают уведомления от бота" },
    { value: "no_show", label: "Были неявки", hint: "Хотя бы один пропущенный визит" },
    { value: "lapsed", label: "Давно не были", hint: "Больше 60 дней без визита и без записи" },
  ];
  readonly clientSortOptions: SelectOption[] = [
    { value: "recent", label: "Недавние" },
    { value: "visits", label: "Больше визитов" },
    { value: "spent", label: "Больше потратили" },
    { value: "name", label: "По имени" },
  ];
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
      id: "clients",
      label: "Клиенты",
      group: "",
      icon: "M16 19v-1a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v1 M9 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6 M22 19v-1a4 4 0 0 0-3-3.9 M16 4.1a3 3 0 0 1 0 5.8",
      description: "Гости студии: история визитов, контакты и сегменты",
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
    this.loadTab();
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
    this.loadTab();
  }
  /** Lists that live outside the daily dashboard load when their tab opens. */
  private loadTab() {
    if (!this.logged) return;
    if (this.tab === "bookings") void this.loadBookings();
    if (this.tab === "clients") void this.loadClients();
  }
  get page() {
    return this.menu.find((m) => m.id === this.tab)!;
  }
  get dateSection() {
    return (
      ["overview", "schedule"].includes(this.tab) ||
      (this.tab === "bookings" && this.bq.period === "day")
    );
  }
  get specialistOptions(): SelectOption[] {
    return [
      { value: 0, label: "Все специалисты" },
      ...(this.data?.catalog.specialists || []).map((s) => ({
        value: s.id,
        label: s.name,
        hint: s.active ? undefined : "Неактивен",
      })),
    ];
  }
  get serviceOptions(): SelectOption[] {
    return [
      { value: 0, label: "Все услуги" },
      ...(this.data?.catalog.services || []).map((s) => ({
        value: s.id,
        label: s.name,
        hint: s.active ? undefined : "Скрыта",
      })),
    ];
  }
  get shiftMasterOptions(): SelectOption[] {
    return (this.data?.catalog.specialists || []).map((s) => ({
      value: s.id,
      label: s.name,
      hint: s.active ? undefined : "Неактивен — запись к нему закрыта",
    }));
  }
  /** Number of narrowing filters, for the reset button. */
  get activeBookingFilters() {
    const b = this.bq;
    return [b.q.trim(), b.status, b.specialist, b.service, b.phone, b.period !== "week"].filter(Boolean).length;
  }
  get activeClientFilters() {
    return [this.cq.q.trim(), this.cq.segment !== "all", this.cq.sort !== "recent"].filter(Boolean).length;
  }
  /** Salon-local date range for the selected period; empty bounds mean open-ended. */
  private bookingRange(): { from: string; to: string } {
    const tz = this.catalog.timezone;
    switch (this.bq.period) {
      case "day":
        return { from: this.date, to: this.date };
      case "today":
        return { from: salonDate(tz), to: salonDate(tz) };
      case "week":
        return { from: salonDate(tz), to: salonDate(tz, 6) };
      case "month":
        return { from: salonDate(tz), to: salonDate(tz, 29) };
      case "past":
        return { from: salonDate(tz, -30), to: salonDate(tz, -1) };
      case "custom":
        return { from: this.bq.from, to: this.bq.to };
      default:
        return { from: "", to: "" };
    }
  }
  /** Debounced reload for text fields; selects reload immediately. */
  searchSoon(kind: "bookings" | "clients") {
    clearTimeout(this.searchTimer);
    this.searchTimer = setTimeout(() => void (kind === "bookings" ? this.loadBookings() : this.loadClients()), 300);
  }
  async loadBookings(more = false) {
    const seq = ++this.bookingSeq;
    const { from, to } = this.bookingRange();
    if (this.bq.period === "custom" && from && to && from > to) {
      this.error = "Дата начала периода позже даты окончания";
      return;
    }
    const p = new URLSearchParams();
    const b = this.bq;
    if (b.q.trim()) p.set("q", b.q.trim());
    if (b.status) p.set("status", b.status);
    if (b.specialist) p.set("specialist_id", String(b.specialist));
    if (b.service) p.set("service_id", String(b.service));
    if (b.phone) p.set("phone", b.phone);
    if (from) p.set("from", from);
    if (to) p.set("to", to);
    p.set("sort", b.sort);
    p.set("limit", "50");
    p.set("offset", String(more ? this.bookingPage.items.length : 0));
    this.bookingsLoading = true;
    this.cd.markForCheck();
    try {
      const page = await api<Page<Booking>>("/admin/bookings?" + p);
      if (seq !== this.bookingSeq) return; // a newer search already started
      this.bookingPage = more ? { ...page, items: [...this.bookingPage.items, ...page.items] } : page;
    } catch (e) {
      if (seq === this.bookingSeq) this.error = this.message(e);
    } finally {
      if (seq === this.bookingSeq) this.bookingsLoading = false;
      this.cd.markForCheck();
    }
  }
  async loadClients(more = false) {
    const seq = ++this.clientSeq;
    const p = new URLSearchParams({ segment: this.cq.segment, sort: this.cq.sort, limit: "50", offset: String(more ? this.clientPage.items.length : 0) });
    if (this.cq.q.trim()) p.set("q", this.cq.q.trim());
    this.clientsLoading = true;
    this.cd.markForCheck();
    try {
      const page = await api<Page<Client>>("/admin/clients?" + p);
      if (seq !== this.clientSeq) return;
      this.clientPage = more ? { ...page, items: [...this.clientPage.items, ...page.items] } : page;
    } catch (e) {
      if (seq === this.clientSeq) this.error = this.message(e);
    } finally {
      if (seq === this.clientSeq) this.clientsLoading = false;
      this.cd.markForCheck();
    }
  }
  resetBookingFilters() {
    this.bq = { ...this.bq, q: "", period: "week", from: "", to: "", status: "", specialist: 0, service: 0, sort: "asc", phone: "", phoneLabel: "" };
    void this.loadBookings();
  }
  resetClientFilters() {
    this.cq = { q: "", segment: "all", sort: "recent" };
    void this.loadClients();
  }
  /** Opens the bookings list narrowed to one client. */
  showClientBookings(c: { phone: string; name: string }) {
    this.bq = { ...this.bq, q: "", status: "", specialist: 0, service: 0, period: "all", sort: "desc", phone: c.phone.replace(/\D/g, ""), phoneLabel: c.name };
    this.select("bookings");
  }
  clearClientFilter() {
    this.bq = { ...this.bq, phone: "", phoneLabel: "" };
    void this.loadBookings();
  }
  when(b: Booking) {
    return dateLabel(b.starts_at, this.catalog.timezone);
  }
  day(v: string | null) {
    return v
      ? new Intl.DateTimeFormat("ru", { timeZone: this.catalog.timezone, day: "numeric", month: "short", year: "numeric" }).format(new Date(v))
      : "—";
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
      this.loadTab();
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
    void this.run(async () => {
      await this.reload();
      this.loadTab();
    });
  }
  login() {
    void this.run(async () => {
      await api("/admin/login", "POST", { password: this.password });
      this.password = "";
      this.logged = true;
      await Promise.all([this.reload(), this.loadBot()]);
      this.loadTab();
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
    void this.run(async () => {
      await api("/admin/bookings/" + id + "/status", "POST", { status });
      await this.reload();
      if (this.tab === "bookings") await this.loadBookings();
      this.notice = status === "cancelled" ? "Запись отменена, клиент получит уведомление" : "Статус записи обновлён";
    });
  }
  askCancel(b: Booking) {
    this.confirmCancel = b;
  }
  confirmCancelBooking() {
    const b = this.confirmCancel;
    this.confirmCancel = null;
    if (b) this.status(b.id, "cancelled");
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
