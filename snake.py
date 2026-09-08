import random
import sys
from typing import Optional

import pygame


Position = tuple[int, int]
Direction = tuple[int, int]


class Snake:
    """Хранит тело змейки и управляет ее движением."""

    def __init__(self, start: Position, cell_size: int) -> None:
        self.cell_size = cell_size
        self.start = start
        self.reset()

    def reset(self) -> None:
        x, y = self.start
        self.body = [
            (x, y),
            (x - self.cell_size, y),
            (x - 2 * self.cell_size, y),
        ]
        self.direction: Direction = (self.cell_size, 0)
        self.next_direction = self.direction

    @property
    def head(self) -> Position:
        return self.body[0]

    def set_direction(self, direction: Direction) -> None:
        """Не позволяет змейке развернуться в противоположную сторону."""
        if (
            direction[0] + self.direction[0] != 0
            or direction[1] + self.direction[1] != 0
        ):
            self.next_direction = direction

    def next_head(self) -> Position:
        self.direction = self.next_direction
        return (
            self.head[0] + self.direction[0],
            self.head[1] + self.direction[1],
        )

    def move(self, position: Position, grow: bool = False) -> None:
        self.body.insert(0, position)
        if not grow:
            self.body.pop()

    def draw(self, screen: pygame.Surface) -> None:
        for index, (x, y) in enumerate(self.body):
            color = (55, 220, 95) if index == 0 else (30, 165, 70)
            rect = pygame.Rect(x, y, self.cell_size, self.cell_size)
            pygame.draw.rect(screen, color, rect, border_radius=5)
            pygame.draw.rect(
                screen,
                (10, 80, 35),
                rect,
                width=1,
                border_radius=5,
            )


class Food:
    """Создает и рисует еду."""

    def __init__(self, cell_size: int) -> None:
        self.cell_size = cell_size
        self.position: Optional[Position] = None

    def spawn(
        self,
        width: int,
        height: int,
        forbidden: set[Position],
    ) -> None:
        free_cells = [
            (x, y)
            for x in range(0, width, self.cell_size)
            for y in range(0, height, self.cell_size)
            if (x, y) not in forbidden
        ]
        self.position = random.choice(free_cells) if free_cells else None

    def draw(self, screen: pygame.Surface) -> None:
        if self.position is None:
            return

        center = (
            self.position[0] + self.cell_size // 2,
            self.position[1] + self.cell_size // 2,
        )
        pygame.draw.circle(screen, (235, 55, 70), center, 7)
        pygame.draw.circle(screen, (255, 150, 150), center, 7, 2)


class Obstacle:
    """Хранит три неподвижных препятствия."""

    def __init__(self, cell_size: int, count: int = 3) -> None:
        self.cell_size = cell_size
        self.count = count
        self.positions: list[Position] = []

    def respawn(
        self,
        width: int,
        height: int,
        forbidden: set[Position],
    ) -> None:
        free_cells = [
            (x, y)
            for x in range(0, width, self.cell_size)
            for y in range(0, height, self.cell_size)
            if (x, y) not in forbidden
        ]
        random.shuffle(free_cells)
        self.positions = free_cells[: self.count]

    def contains(self, position: Position) -> bool:
        return position in self.positions

    def draw(self, screen: pygame.Surface) -> None:
        for x, y in self.positions:
            rect = pygame.Rect(x, y, self.cell_size, self.cell_size)
            pygame.draw.rect(screen, (105, 105, 120), rect, border_radius=4)
            pygame.draw.rect(
                screen,
                (190, 190, 205),
                rect,
                width=2,
                border_radius=4,
            )


class Game:
    """Управляет окном, игровым циклом, счетом и состояниями игры."""

    WIDTH = 800
    HEIGHT = 600
    CELL_SIZE = 20
    FPS = 12
    OBSTACLE_INTERVAL_MS = 15_000

    def __init__(self) -> None:
        pygame.init()
        pygame.display.set_caption("Змейка")
        self.screen = pygame.display.set_mode((self.WIDTH, self.HEIGHT))
        self.clock = pygame.time.Clock()
        self.font = pygame.font.SysFont("arial", 24, bold=True)
        self.title_font = pygame.font.SysFont("arial", 54, bold=True)
        self.small_font = pygame.font.SysFont("arial", 22)

        self.blue_portal = (100, 280)
        self.orange_portal = (680, 280)
        self.high_score = 0
        self.running = True
        self.reset()

    def reset(self) -> None:
        self.snake = Snake(
            (self.WIDTH // 2, self.HEIGHT // 2),
            self.CELL_SIZE,
        )
        self.food = Food(self.CELL_SIZE)
        self.obstacle = Obstacle(self.CELL_SIZE)
        self.score = 0
        self.game_over = False
        self.last_obstacle_change = pygame.time.get_ticks()
        self.respawn_obstacles()
        self.spawn_food()

    def wrap(self, position: Position) -> Position:
        return position[0] % self.WIDTH, position[1] % self.HEIGHT

    def teleport(self, position: Position) -> Position:
        if position == self.blue_portal:
            return self.orange_portal
        if position == self.orange_portal:
            return self.blue_portal
        return position

    def forbidden_cells(self) -> set[Position]:
        return (
            set(self.snake.body)
            | set(self.obstacle.positions)
            | {self.blue_portal, self.orange_portal}
        )

    def spawn_food(self) -> None:
        self.food.spawn(self.WIDTH, self.HEIGHT, self.forbidden_cells())

    def respawn_obstacles(self) -> None:
        next_position = self.wrap(self.snake.next_head())
        forbidden = (
            set(self.snake.body)
            | {self.blue_portal, self.orange_portal, next_position}
        )
        self.obstacle.respawn(self.WIDTH, self.HEIGHT, forbidden)
        self.last_obstacle_change = pygame.time.get_ticks()

    def handle_events(self) -> None:
        for event in pygame.event.get():
            if event.type == pygame.QUIT:
                self.running = False
            elif event.type == pygame.KEYDOWN:
                if self.game_over:
                    if event.key == pygame.K_r:
                        self.reset()
                    elif event.key == pygame.K_ESCAPE:
                        self.running = False
                    continue

                directions = {
                    pygame.K_UP: (0, -self.CELL_SIZE),
                    pygame.K_w: (0, -self.CELL_SIZE),
                    pygame.K_DOWN: (0, self.CELL_SIZE),
                    pygame.K_s: (0, self.CELL_SIZE),
                    pygame.K_LEFT: (-self.CELL_SIZE, 0),
                    pygame.K_a: (-self.CELL_SIZE, 0),
                    pygame.K_RIGHT: (self.CELL_SIZE, 0),
                    pygame.K_d: (self.CELL_SIZE, 0),
                }
                if event.key in directions:
                    self.snake.set_direction(directions[event.key])

    def update(self) -> None:
        if self.game_over:
            return

        now = pygame.time.get_ticks()
        if now - self.last_obstacle_change >= self.OBSTACLE_INTERVAL_MS:
            self.respawn_obstacles()

        next_head = self.teleport(self.wrap(self.snake.next_head()))
        if self.obstacle.contains(next_head):
            self.game_over = True
            return

        # Хвост покинет последнюю клетку в этом ходу, поэтому ее проверяем.
        if next_head in self.snake.body[1:-1]:
            self.game_over = True
            return

        eating = next_head == self.food.position
        self.snake.move(next_head, grow=eating)
        if eating:
            self.score += 1
            self.high_score = max(self.high_score, self.score)
            self.spawn_food()

    def draw_grid(self) -> None:
        for x in range(0, self.WIDTH, self.CELL_SIZE):
            pygame.draw.line(
                self.screen,
                (28, 31, 42),
                (x, 0),
                (x, self.HEIGHT),
            )
        for y in range(0, self.HEIGHT, self.CELL_SIZE):
            pygame.draw.line(
                self.screen,
                (28, 31, 42),
                (0, y),
                (self.WIDTH, y),
            )

    def draw_portal(self, position: Position, color: tuple[int, int, int]) -> None:
        center = (
            position[0] + self.CELL_SIZE // 2,
            position[1] + self.CELL_SIZE // 2,
        )
        pygame.draw.circle(screen := self.screen, color, center, 9)
        pygame.draw.circle(screen, (255, 255, 255), center, 5, 2)

    def draw_hud(self) -> None:
        score = self.font.render(f"Счет: {self.score}", True, (240, 240, 245))
        high_score = self.font.render(
            f"Рекорд: {self.high_score}",
            True,
            (240, 240, 245),
        )
        self.screen.blit(score, (15, 12))
        self.screen.blit(high_score, (180, 12))

    def draw_game_over(self) -> None:
        overlay = pygame.Surface((self.WIDTH, self.HEIGHT))
        overlay.set_alpha(190)
        overlay.fill((0, 0, 0))
        self.screen.blit(overlay, (0, 0))

        title = self.title_font.render("ИГРА ОКОНЧЕНА", True, (255, 90, 90))
        score = self.font.render(
            f"Счет: {self.score}    Рекорд: {self.high_score}",
            True,
            (240, 240, 245),
        )
        hint = self.small_font.render(
            "R — начать заново, Esc — выйти",
            True,
            (240, 240, 245),
        )
        self.screen.blit(title, title.get_rect(center=(400, 235)))
        self.screen.blit(score, score.get_rect(center=(400, 315)))
        self.screen.blit(hint, hint.get_rect(center=(400, 365)))

    def draw(self) -> None:
        self.screen.fill((18, 20, 28))
        self.draw_grid()
        self.draw_portal(self.blue_portal, (50, 130, 255))
        self.draw_portal(self.orange_portal, (245, 145, 45))
        self.food.draw(self.screen)
        self.obstacle.draw(self.screen)
        self.snake.draw(self.screen)
        self.draw_hud()
        if self.game_over:
            self.draw_game_over()
        pygame.display.flip()

    def run(self) -> None:
        while self.running:
            self.handle_events()
            self.update()
            self.draw()
            self.clock.tick(self.FPS)
        pygame.quit()
        sys.exit()


if __name__ == "__main__":
    Game().run()
