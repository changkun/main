// particle.js
var particles = particlesJS("particles", {
  "particles": {
    "number": {
      "value": 40
    },
    "color": {
      "value": "#999"
    },
    "opacity": {
      "value": 0.5,
      "random": true,
      "anim": {
        "enable": true,
        "speed": 0.5,
        "opacity_min": 0.1,
        "sync": true
      }
    },
    "size": {
      "value": 15,
      "random": true,
      "anim": {
        "enable": false,
        "speed": 10,
        "size_min": 0.1,
        "sync": true
      }
    },
    "line_linked": {
      "enable": true,
      "distance": 150,
      "color": "#999",
      "opacity": 0.4,
      "width": 1
    },
    "move": {
      "enable": true,
      "speed": 1,
      "direction": "none",
      "random": false,
      "straight": false,
      "out_mode": "out",
      "bounce": false,
      "attract": {
        "enable": false,
        "rotateX": 600,
        "rotateY": 1200
      }
    }
  },
  "interactivity": {
    "detect_on": "window",
    "events": {
      "onhover": {
        "enable": true,
        "mode": "grab"
      },
      "onclick": {
        "enable": true,
        "mode": "push"
      },
      "resize": true
    },
    "modes": {
      "grab": {
        "distance": 140,
        "line_linked": {
          "opacity": 1
        }
      },
      "bubble": {
        "distance": 400,
        "size": 40,
        "duration": 2,
        "opacity": 8,
        "speed": 3
      },
      "repulse": {
        "distance": 200,
        "duration": 0.4
      },
      "push": {
        "particles_nb": 4
      },
      "remove": {
        "particles_nb": 2
      }
    }
  },
  "retina_detect": true
});
