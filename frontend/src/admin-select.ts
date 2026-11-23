import { ChangeDetectionStrategy, Component, computed, input, model } from "@angular/core";
import { HlmSelectImports } from "@spartan-ng/helm/select";

export type SelectValue = string | number;
export interface SelectOption {
  value: SelectValue;
  label: string;
  /** Second line under the label, e.g. what a segment means. */
  hint?: string;
  /** CSS color for a status dot shown before the label (in the list and the trigger). */
  dot?: string;
}

let nextSelectId = 0;

/**
 * Spartan select used across the admin: a popover listbox with keyboard navigation,
 * check marks, optional status dots and hints. An empty value shows the placeholder.
 */
@Component({
  selector: "app-select",
  standalone: true,
  imports: [HlmSelectImports],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: "block" },
  template: `
    <hlm-select
      [value]="value()"
      (valueChange)="value.set($any($event))"
      [disabled]="disabled()"
      [itemToString]="labelOf"
    >
      <hlm-select-trigger class="w-full bg-card" [buttonId]="inputId()" [size]="size()">
        <span hlmSelectPlaceholder class="text-muted-foreground">{{ placeholder() }}</span>
        <ng-template hlmSelectValueTemplate let-current>
          <!-- An empty value is shown by the placeholder above, not twice. -->
          @if (current !== "") {
          <span class="flex min-w-0 items-center gap-2">
            @if (option(current)?.dot; as color) {
              <span class="size-2 shrink-0 rounded-full" [style.background]="color"></span>
            }
            <span class="truncate">{{ option(current)?.label ?? placeholder() }}</span>
          </span>
          }
        </ng-template>
      </hlm-select-trigger>
      <hlm-select-content *hlmSelectPortal class="min-w-56">
        @for (o of options(); track o.value) {
          <hlm-select-item [value]="o.value" class="py-2">
            <span class="flex min-w-0 items-start gap-2.5">
              @if (o.dot) {
                <span class="mt-1.5 size-2 shrink-0 rounded-full" [style.background]="o.dot"></span>
              }
              <span class="grid min-w-0 gap-0.5">
                <span class="truncate">{{ o.label }}</span>
                @if (o.hint) {
                  <span class="text-muted-foreground text-xs leading-snug whitespace-normal">{{ o.hint }}</span>
                }
              </span>
            </span>
          </hlm-select-item>
        }
      </hlm-select-content>
    </hlm-select>
  `,
})
export class AppSelect {
  readonly value = model<SelectValue>("");
  readonly options = input<SelectOption[]>([]);
  readonly placeholder = input("Выберите");
  readonly disabled = input(false);
  readonly size = input<"default" | "sm">("default");
  /** id of the trigger button, for <label for>. */
  readonly inputId = input<string>(`app-select-${nextSelectId++}`);

  private readonly byValue = computed(() => new Map(this.options().map((o) => [o.value, o])));
  protected option(v: SelectValue) {
    return this.byValue().get(v);
  }
  protected labelOf = (v: SelectValue) => this.byValue().get(v)?.label ?? "";
}
