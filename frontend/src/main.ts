import { Component } from "@angular/core";
import { bootstrapApplication } from "@angular/platform-browser";
import { AdminComponent } from "./admin";

@Component({
  selector: "app-root",
  standalone: true,
  imports: [AdminComponent],
  templateUrl: "./app.html",
})
class App {
  constructor() {
    if (location.pathname !== "/admin" || location.search)
      history.replaceState(null, "", "/admin" + location.hash);
  }
}
bootstrapApplication(App).catch(console.error);
